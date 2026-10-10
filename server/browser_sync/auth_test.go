package browsersync

import (
 "context"
 "crypto/ecdsa"
 "crypto/elliptic"
 "crypto/rand"
 "crypto/sha256"
 "crypto/x509"
 "database/sql"
 "encoding/base64"
 "math/big"
 "testing"
 "time"

 _ "modernc.org/sqlite"
)

func testBrowserStore(t *testing.T)*Store{
 t.Helper()
 db,err:=sql.Open("sqlite",":memory:")
 if err!=nil{t.Fatal(err)}
 t.Cleanup(func(){_ =db.Close()})
 db.SetMaxOpenConns(1)
 if _,err:=db.Exec(`CREATE TABLE identities(id TEXT PRIMARY KEY,status TEXT NOT NULL)`);err!=nil{t.Fatal(err)}
 if _,err:=db.Exec(`INSERT INTO identities(id,status) VALUES('id_1','active')`);err!=nil{t.Fatal(err)}
 store,err:=NewStore(db);if err!=nil{t.Fatal(err)}
 return store
}
func makeKey(t *testing.T)(*ecdsa.PrivateKey,string){
 t.Helper()
 key,err:=ecdsa.GenerateKey(elliptic.P256(),rand.Reader);if err!=nil{t.Fatal(err)}
 der,err:=x509.MarshalPKIXPublicKey(&key.PublicKey);if err!=nil{t.Fatal(err)}
 return key,base64.RawURLEncoding.EncodeToString(der)
}
func createApprovedBrowser(t *testing.T,store *Store)(BrowserDevice,*ecdsa.PrivateKey){
 t.Helper()
 key,spki:=makeKey(t);_,encSpki:=makeKey(t)
 d:=BrowserDevice{
   ID:"browser_69064be2-ea88-4e8d-9aa1-a0c55e7a0d5e",
   Name:"Test Chrome",SigningKey:spki,EncryptionKey:encSpki,
 }
 if err:=ValidateRegistration(d);err!=nil{t.Fatal(err)}
 inserted,err:=store.Register(context.Background(),d);if err!=nil{t.Fatal(err)}
 if inserted.State!="pending"{t.Fatal("browser should start pending")}
 if err:=store.Approve(context.Background(),d.ID,"id_1",true,false);err!=nil{t.Fatal(err)}
 return d,key
}
func signatureFor(t *testing.T,key *ecdsa.PrivateKey,origin,id,nonce string)string{
 t.Helper()
 msg:=[]byte("browser.sync.v1\n"+origin+"\n"+id+"\n"+nonce)
 digest:=sha256.Sum256(msg)
 r,s,err:=ecdsa.Sign(rand.Reader,key,digest[:]);if err!=nil{t.Fatal(err)}
 encoded:=make([]byte,64)
 r.FillBytes(encoded[:32]);s.FillBytes(encoded[32:])
 return base64.RawURLEncoding.EncodeToString(encoded)
}
func TestBrowserChallengeAuthenticateAndReplay(t *testing.T){
 store:=testBrowserStore(t)
 d,key:=createApprovedBrowser(t,store)
 a:=NewAuthenticator(store)
 nonce,err:=a.Challenge(d.ID);if err!=nil{t.Fatal(err)}
 sig:=signatureFor(t,key,"https://relay.example.com",d.ID,nonce)
 verified,err:=a.Verify(context.Background(),d.ID,"https://relay.example.com",nonce,sig)
 if err!=nil||verified.ID!=d.ID{t.Fatalf("failed auth: %v",err)}
 if _,err:=a.Verify(context.Background(),d.ID,"https://relay.example.com",nonce,sig);err==nil{
  t.Fatal("reused challenge was accepted")
 }
}
func TestBrowserAuthRejectsBadSignatureAndPending(t *testing.T){
 store:=testBrowserStore(t)
 d,key:=createApprovedBrowser(t,store)
 a:=NewAuthenticator(store)
 nonce,_:=a.Challenge(d.ID)
 sig:=signatureFor(t,key,"https://malicious.example",d.ID,nonce)
 if _,err:=a.Verify(context.Background(),d.ID,"https://relay.example.com",nonce,sig);err==nil{
  t.Fatal("signature for wrong origin was accepted")
 }
 if err:=store.Revoke(context.Background(),d.ID);err!=nil{t.Fatal(err)}
 nonce,_=a.Challenge(d.ID)
 sig=signatureFor(t,key,"https://relay.example.com",d.ID,nonce)
 if _,err:=a.Verify(context.Background(),d.ID,"https://relay.example.com",nonce,sig);err==nil{
  t.Fatal("revoked device authorized")
 }
}
func TestBrowserRegistrationCannotReplaceKey(t *testing.T){
 store:=testBrowserStore(t)
 d,_:=createApprovedBrowser(t,store)
 _,another:=makeKey(t)
 d.SigningKey=another
 if _,err:=store.Register(context.Background(),d);err==nil{t.Fatal("key replacement allowed")}
}
func TestBrowserChallengeHasExpiry(t *testing.T){
 store:=testBrowserStore(t)
 a:=NewAuthenticator(store)
 const id="browser_69064be2-ea88-4e8d-9aa1-a0c55e7a0d5e"
 nonce,err:=a.Challenge(id);if err!=nil{t.Fatal(err)}
 a.mu.Lock()
 a.challenges[id]=challenge{nonce:nonce,deadline:time.Now().Add(-time.Second)}
 a.mu.Unlock()
 if _,err:=a.Verify(context.Background(),id,"https://relay.example.com",nonce,
  base64.RawURLEncoding.EncodeToString(new(big.Int).Bytes()));err==nil{
  t.Fatal("expired challenge allowed")
 }
}
