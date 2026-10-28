package neopool

import (
 "bytes"
 "encoding/json"
 "io"
 "net/http"
 "net/url"
 "time"

 "github.com/gin-gonic/gin"
)

// loginWriter buffers only the initial engine login response so the initiating
// user's state binding commits before its authorization URL becomes visible.
type loginWriter struct {gin.ResponseWriter;body bytes.Buffer;status int}
func (w *loginWriter) WriteHeader(status int){w.status=status}
func (w *loginWriter) WriteHeaderNow(){}
func (w *loginWriter) Write(b []byte)(int,error){if w.body.Len()+len(b)>65536{return 0,io.ErrShortBuffer};return w.body.Write(b)}
func (w *loginWriter) WriteString(text string)(int,error){return w.Write([]byte(text))}
func (w *loginWriter) Status()int{if w.status==0{return 200};return w.status}
func (w *loginWriter) Size()int{return w.body.Len()}
func (w *loginWriter) Written()bool{return w.body.Len()>0}
func (w *loginWriter) Flush(){}

func (h *HTTP) login(c *gin.Context){
 p,ok:=ownerSession(c);if !ok{return};if h.management==nil{poolError(c,503,"engine login is unavailable");return}
 provider:=c.Param("provider");switch provider{case "codex","anthropic","claude","gemini","gemini-cli","antigravity","qwen","iflow","kimi","kimi-ai","devin","xai","meta":default:poolError(c,400,"unsupported login provider");return}
 dispatch:=provider;if dispatch=="anthropic"{dispatch="claude"}
 // Owners cannot smuggle engine config, filenames, account IDs, or SIWC bind
 // targets through this narrow new-login operation.
 c.Request.URL.RawQuery=url.Values{"provider":{dispatch},"is_webui":{"true"}}.Encode()
 original:=c.Writer;captured:=&loginWriter{ResponseWriter:original};c.Writer=captured
 h.management.StartOAuthV8(c)
 c.Writer=original
 if captured.Status()>=400{poolError(c,captured.Status(),"provider login is unavailable");return}
 var body struct{State string `json:"state"`};if err:=json.Unmarshal(captured.body.Bytes(),&body);err!=nil || body.State==""{poolError(c,502,"engine login returned no session state");return}
 tx,err:=h.pool.db.BeginTx(c.Request.Context(),nil);if err!=nil{respondError(c,err);return};defer tx.Rollback()
 if _,err=tx.ExecContext(c.Request.Context(),`DELETE FROM neo_pool_logins WHERE created_at<?`,h.pool.now().Add(-15*time.Minute).UnixMilli());err==nil{_,err=tx.ExecContext(c.Request.Context(),`INSERT INTO neo_pool_logins(state,user_id,provider_id,created_at) VALUES(?,?,?,?)`,body.State,p.UserID,provider,h.pool.now().UnixMilli())};if err!=nil{respondError(c,err);return};if err=tx.Commit();err!=nil{respondError(c,err);return}
 c.Data(captured.Status(),"application/json; charset=utf-8",captured.body.Bytes())
}

func (h *HTTP) loginOwned(c *gin.Context,state string)(string,bool){
 p,ok:=ownerSession(c);if !ok{return "",false};if h.management==nil{poolError(c,503,"engine login is unavailable");return "",false}
 var provider string;err:=h.pool.db.QueryRowContext(c.Request.Context(),`SELECT provider_id FROM neo_pool_logins WHERE state=? AND user_id=? AND created_at>=?`,state,p.UserID,h.pool.now().Add(-15*time.Minute).UnixMilli()).Scan(&provider)
 if err!=nil{poolError(c,404,"login state is unknown or unavailable");return "",false};return provider,true
}
func (h *HTTP) loginStatus(c *gin.Context){state:=c.Query("state");if _,ok:=h.loginOwned(c,state);!ok{return};c.Request.URL.RawQuery=url.Values{"state":{state}}.Encode();h.management.GetAuthStatus(c)}
func (h *HTTP) loginCallback(c *gin.Context){
 var body struct{Provider string `json:"provider"`;State string `json:"state"`;Code string `json:"code"`;Error string `json:"error"`}
 if c.ShouldBindJSON(&body)!=nil || body.State==""{poolError(c,http.StatusBadRequest,"login state is required");return}
 provider,ok:=h.loginOwned(c,body.State);if !ok{return};if body.Provider!="" && body.Provider!=provider{poolError(c,403,"login provider does not match initiation");return};body.Provider=provider;if body.Provider=="claude"{body.Provider="anthropic"}
 encoded,err:=json.Marshal(body);if err!=nil{respondError(c,err);return};c.Request.Body=io.NopCloser(bytes.NewReader(encoded));c.Request.ContentLength=int64(len(encoded));h.management.PostOAuthCallbackV8(c)
}
