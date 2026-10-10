package browsersync

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

func approvedDeliveryRule(t *testing.T, store *Store) (BrowserDevice, BrowserDevice, string) {
	t.Helper()
	source, _ := approvedWSSBrowser(t, store, true, false)
	target, _ := approvedWSSBrowser(t, store, false, true)
	ruleID := uuid.NewString()
	if err := store.OfferRule(context.Background(), source.ID, encryptedTestOffer(ruleID, target.ID)); err != nil { t.Fatal(err) }
	if err := store.AcceptRule(context.Background(), target.ID, ruleID); err != nil { t.Fatal(err) }
	if err := store.ConfirmRule(context.Background(), source.ID, ruleID); err != nil { t.Fatal(err) }
	return source, target, ruleID
}

func TestDeliveryAckRequiresRecordedMessageAndCorrectReceiver(t *testing.T) {
	store := testBrowserStore(t)
	source, target, ruleID := approvedDeliveryRule(t, store)
	ctx := context.Background()
	now := time.Now().UTC()
	forged := uuid.NewString()
	if _, err := store.ClaimDeliveryReceipt(ctx,target.ID,ruleID,forged,"APPLIED",now); !errors.Is(err,ErrRuleDenied) {
		t.Fatalf("fabricated ACK succeeded: %v",err)
	}
	third, _ := approvedWSSBrowser(t,store,false,true)
	messageID := uuid.NewString()
	if err := store.RecordDelivery(ctx,messageID,ruleID,source.ID,target.ID,now); err != nil { t.Fatal(err) }
	if _, err := store.ClaimDeliveryReceipt(ctx,third.ID,ruleID,messageID,"APPLIED",now); !errors.Is(err,ErrRuleDenied) {
		t.Fatalf("wrong receiver ACK succeeded: %v",err)
	}
	if _, err := store.ClaimDeliveryReceipt(ctx,target.ID,ruleID,messageID,"unknown",now); !errors.Is(err,ErrRuleDenied) {
		t.Fatalf("unapproved status accepted: %v",err)
	}
	ackSource, err := store.ClaimDeliveryReceipt(ctx,target.ID,ruleID,messageID,"RECEIVED",now)
	if err != nil || ackSource != source.ID { t.Fatalf("first receipt failed: %v",err) }
	if _, err := store.ClaimDeliveryReceipt(ctx,target.ID,ruleID,messageID,"RECEIVED",now); !errors.Is(err,ErrRuleDenied) {
		t.Fatalf("duplicate RECEIVED accepted: %v",err)
	}
	ackSource, err = store.ClaimDeliveryReceipt(ctx,target.ID,ruleID,messageID,"APPLIED",now)
	if err != nil || ackSource != source.ID { t.Fatalf("terminal ACK failed: %v",err) }
	if _, err := store.ClaimDeliveryReceipt(ctx,target.ID,ruleID,messageID,"FAILED",now); !errors.Is(err,ErrRuleDenied) {
		t.Fatalf("duplicate terminal ACK accepted: %v",err)
	}
	count, err := store.ActiveDeliveryCount(ctx,ruleID)
	if err != nil || count != 0 { t.Fatalf("delivery receipt not consumed: count=%d err=%v",count,err) }
}

func TestDeliveryExpiresAndRuleRevocationPreventsAck(t *testing.T) {
	store := testBrowserStore(t)
	source, target, ruleID := approvedDeliveryRule(t,store)
	ctx := context.Background()
	old := time.Now().UTC().Add(-20*time.Minute)
	msg := uuid.NewString()
	if err := store.RecordDelivery(ctx,msg,ruleID,source.ID,target.ID,old); err != nil { t.Fatal(err) }
	if _, err := store.ClaimDeliveryReceipt(ctx,target.ID,ruleID,msg,"APPLIED",time.Now().UTC()); !errors.Is(err,ErrRuleDenied) {
		t.Fatalf("expired ACK accepted: %v",err)
	}
	newMsg:=uuid.NewString()
	if err := store.RecordDelivery(ctx,newMsg,ruleID,source.ID,target.ID,time.Now().UTC()); err != nil { t.Fatal(err) }
	if err := store.RevokeRule(ctx,target.ID,ruleID); err != nil { t.Fatal(err) }
	if _, err := store.ClaimDeliveryReceipt(ctx,target.ID,ruleID,newMsg,"APPLIED",time.Now().UTC()); !errors.Is(err,ErrRuleDenied) {
		t.Fatalf("ACK accepted after revocation: %v",err)
	}
}

func TestDuplicateDeliveryMessageIDCannotBeReused(t *testing.T) {
	store := testBrowserStore(t)
	source, target, ruleID := approvedDeliveryRule(t,store)
	ctx:=context.Background()
	id:=uuid.NewString()
	if err:=store.RecordDelivery(ctx,id,ruleID,source.ID,target.ID,time.Now().UTC());err!=nil{t.Fatal(err)}
	if err:=store.RecordDelivery(ctx,id,ruleID,source.ID,target.ID,time.Now().UTC());err==nil{
		t.Fatal("same message ID reused")
	}
}
