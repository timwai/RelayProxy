package browsersync

import (
 "context"
 "net/http"
 "net/http/httptest"
 "testing"
)

func TestExtensionAutoEnrollmentRequiresAdminApproval(t *testing.T) {
 store:=testBrowserStore(t)
 const ext="bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
 h:=NewHandler(store,nil)
 request:=func(path string) int {
  req:=httptest.NewRequest(http.MethodPost,"https://relay.example.com"+path,nil)
  req.Header.Set("Origin","chrome-extension://"+ext)
  req.RemoteAddr="203.0.113.100:10301"
  resp:=httptest.NewRecorder()
  h.ServeHTTP(resp,req)
  return resp.Code
 }
 if got:=request("/api/v1/browser-sync/extensions/register");got!=http.StatusAccepted {
  t.Fatalf("extension auto-report failed: %d",got)
 }
 items,err:=store.Extensions(context.Background())
 if err!=nil||len(items)!=1||items[0].ID!=ext||items[0].State!="pending" {
  t.Fatalf("expected pending extension approval: %+v %v",items,err)
 }
 if got:=request("/api/v1/browser-sync/devices/register");got!=http.StatusForbidden {
  t.Fatalf("unapproved extension registered browser: %d",got)
 }
 if err:=store.SetExtensionApproved(context.Background(),ext,true);err!=nil{t.Fatal(err)}
 if got:=request("/api/v1/browser-sync/devices/register");got!=http.StatusBadRequest {
  t.Fatalf("approved extension did not reach signed device registration: %d",got)
 }
 if err:=store.SetExtensionApproved(context.Background(),ext,false);err!=nil{t.Fatal(err)}
 if got:=request("/api/v1/browser-sync/devices/register");got!=http.StatusForbidden {
  t.Fatalf("revoked extension still allowed: %d",got)
 }
}
func TestInvalidExtensionOriginCannotEnroll(t *testing.T){
 store:=testBrowserStore(t)
 h:=NewHandler(store,nil)
 for _,origin:=range []string{"https://example.com","chrome-extension://notvalid","chrome-extension://bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb.extra"}{
  req:=httptest.NewRequest(http.MethodPost,"https://relay.example.com/api/v1/browser-sync/extensions/register",nil)
  req.Header.Set("Origin",origin)
  resp:=httptest.NewRecorder()
  h.ServeHTTP(resp,req)
  if resp.Code!=http.StatusForbidden{t.Fatalf("origin %q accepted: %d",origin,resp.Code)}
 }
}
