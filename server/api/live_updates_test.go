package api

import "testing"

func TestLiveUpdatesScopeIsolationAndAdminDelivery(t *testing.T) {
	h:=newLiveUpdates()
	admin,cancelAdmin,ok:=h.subscribe("",true)
	if !ok {t.Fatal("admin subscribe denied")}
	defer cancelAdmin()
	a,cancelA,ok:=h.subscribe("identity_a",false)
	if !ok {t.Fatal("identity A subscribe denied")}
	defer cancelA()
	b,cancelB,ok:=h.subscribe("identity_b",false)
	if !ok {t.Fatal("identity B subscribe denied")}
	defer cancelB()

	h.publish("identity_a")
	select{
	case <-a:
	default:t.Fatal("identity A missed own notification")
	}
	select{
	case <-admin:
	default:t.Fatal("admin missed identity notification")
	}
	select{
	case <-b:t.Fatal("cross-identity activity leaked to B")
	default:
	}
	h.publish("")
	select{
	case <-admin:
	default:t.Fatal("admin missed global mutation")
	}
	select{
	case <-a:t.Fatal("identity subscriber received admin-only activity")
	default:
	}
	select{
	case <-b:t.Fatal("unrelated subscriber received admin-only activity")
	default:
	}
}

func TestLiveUpdatesSlowConsumersCannotBlockAuthorizationMutations(t *testing.T) {
	h:=newLiveUpdates()
	ch,done,ok:=h.subscribe("id1",false)
	if !ok{t.Fatal("subscribe failed")}
	// The subscriber stops reading, but publishing only queues a single
	// revalidation hint, never unbounded logs or cross-identity data.
	for i:=0;i<1000;i++ {h.publish("id1")}
	select{
	case value:=<-ch:
		if value==0{t.Fatal("missing revision")}
	default:t.Fatal("no bounded invalidation was queued")
	}
	done()
	h.publish("id1") // unsubscribed event cannot enqueue or panic
	if len(h.subscribers)!=0{t.Fatal("subscriber leaked after unsubscribe")}
}

func TestLiveUpdatesGlobalConnectionBudget(t *testing.T) {
	h:=newLiveUpdates()
	releases:=make([]func(),0,maxLiveSubscribers)
	for i:=0;i<maxLiveSubscribers;i++ {
		_,done,ok:=h.subscribe("same_identity",false)
		if !ok{t.Fatalf("unexpected limit at %d",i)}
		releases=append(releases,done)
	}
	if _,_,ok:=h.subscribe("",true);ok{
		t.Fatal("unbounded SSE subscribers allowed")
	}
	for _,done:=range releases{done()}
	if _,done,ok:=h.subscribe("",true);!ok {
		t.Fatal("subscriber budget was not released")
	}else{done()}
}
