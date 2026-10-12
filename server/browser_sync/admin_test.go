package browsersync

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func adminTestPair(t *testing.T, store *Store) (string, string, string) {
	t.Helper()
	ctx := context.Background()
	source, target := "browser_"+uuid.NewString(), "browser_"+uuid.NewString()
	registerBrowserForRules(t, store, source, true, false)
	registerBrowserForRules(t, store, target, false, true)
	id := uuid.NewString()
	if err := store.OfferRule(ctx, source, encryptedTestOffer(id, target)); err != nil {
		t.Fatal(err)
	}
	if err := store.AcceptRule(ctx, target, id); err != nil {
		t.Fatal(err)
	}
	if err := store.ConfirmRule(ctx, source, id); err != nil {
		t.Fatal(err)
	}
	return source, target, id
}

func TestBrowserAdminRulesNeverExposeEncryptedOfferAndRequireSafeTransitions(t *testing.T) {
	store := testBrowserStore(t)
	ctx := context.Background()
	source, target, ruleID := adminTestPair(t, store)
	rules, err := store.AdminRules(ctx)
	if err != nil || len(rules)!=1 || rules[0].Status!="active" ||
		!rules[0].SourceApproved || !rules[0].TargetApproved || !rules[0].KeysConfirmed {
		t.Fatalf("unexpected metadata rules: %+v %v", rules, err)
	}
	encoded, err := json.Marshal(rules)
	if err != nil { t.Fatal(err) }
	for _, secretField := range []string{"offer","ciphertext","policyDigest","signature","signingKey","encryptionKey"} {
		if strings.Contains(string(encoded),secretField) {
			t.Fatalf("browser admin leaks encrypted offer field %q",secretField)
		}
	}
	if err := store.AdminRuleAction(ctx,ruleID,"pause");err!=nil { t.Fatal(err) }
	if active,err:=store.ActiveRule(ctx,ruleID);err!=nil || active.Active {
		t.Fatalf("paused rule still active: %+v %v",active,err)
	}
	if err:=store.AdminRuleAction(ctx,ruleID,"pause");!errors.Is(err,ErrRuleDenied) {
		t.Fatalf("double pause accepted: %v",err)
	}
	if err:=store.AdminRuleAction(ctx,ruleID,"resume");err!=nil {t.Fatal(err)}
	if err:=store.AdminRuleAction(ctx,ruleID,"pause");err!=nil {t.Fatal(err)}
	if err:=store.Revoke(ctx,target);err!=nil {t.Fatal(err)}
	if err:=store.AdminRuleAction(ctx,ruleID,"resume");!errors.Is(err,ErrRuleDenied) {
		t.Fatalf("revoked device pair resumed: %v",err)
	}
	if err:=store.AdminRuleAction(ctx,ruleID,"revoke");!errors.Is(err,ErrRuleDenied) {
		t.Fatalf("device revocation did not revoke pair: %v",err)
	}
}

func TestBrowserAuditStoresOnlyBoundedTerminalMetadataAndPaginates(t *testing.T) {
	store:=testBrowserStore(t)
	ctx:=context.Background()
	source,target,ruleID:=adminTestPair(t,store)
	now:=time.Now().UTC()
	for _, status:=range []string{"APPLIED","FAILED","CONFLICT"} {
		mid:=uuid.NewString()
		if err:=store.RecordDelivery(ctx,mid,ruleID,source,target,now);err!=nil {t.Fatal(err)}
		if _,err:=store.ClaimDeliveryReceipt(ctx,target,ruleID,mid,status,now);err!=nil {t.Fatal(err)}
	}
	all,err:=store.AdminAudit(ctx,0,100)
	if err!=nil {t.Fatal(err)}
	var deliveryCount int
	for _, e:=range all {
		if e.Type=="delivery_result" {
			deliveryCount++
			if e.DeviceID!=source || e.PeerID!=target || e.RuleID!=ruleID {
				t.Fatalf("audit exposes unrelated data: %+v",e)
			}
			switch e.Outcome {case "APPLIED","FAILED","CONFLICT":default:t.Fatal(e)}
		}
	}
	if deliveryCount!=3 {t.Fatalf("expected 3 terminal results; got %d",deliveryCount)}
	first,err:=store.AdminAudit(ctx,0,2)
	if err!=nil || len(first)!=2 {t.Fatalf("first page: %+v %v",first,err)}
	next,err:=store.AdminAudit(ctx,first[1].ID,2)
	if err!=nil || len(next)!=2 || next[0].ID>=first[1].ID {t.Fatalf("audit cursor overlaps: %+v %v",next,err)}
	if _,err:=store.AdminAudit(ctx,-1,50);err==nil {t.Fatal("negative cursor accepted")}
	if _,err:=store.AdminAudit(ctx,0,101);err==nil {t.Fatal("overlarge page accepted")}
	stats,err:=store.AdminDeviceStats(ctx)
	if err!=nil {t.Fatal(err)}
	for _, d:=range stats {
		if d.DeviceID==source || d.DeviceID==target {
			if d.Applied!=1 || d.Failed!=1 || d.Conflicted!=1 || d.LastSyncAt==nil {
				t.Fatalf("invalid activity for %s: %+v",d.DeviceID,d)
			}
		}
	}
}
