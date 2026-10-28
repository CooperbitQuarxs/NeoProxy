package neopool

import (
 "context"
 "fmt"
 "sync"

 "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

// AttemptUsage captures actual executor reports synchronously. It never trusts
// the resolver's proposed account in place of the executor's recorded AuthID.
type AttemptUsage struct {mu sync.Mutex;id string;records map[string]attemptRecord;err error}
type attemptRecord struct{account string;input,output,cached int64;known bool}

func WithAttempt(ctx context.Context,id string)(context.Context,*AttemptUsage){
 capture:=&AttemptUsage{id:id,records:map[string]attemptRecord{}}
 return usage.WithObserver(ctx,capture.observe),capture
}
func (a *AttemptUsage) observe(record usage.Record){
 a.mu.Lock();defer a.mu.Unlock()
 if record.AuthID=="" || record.RequestID==""{a.err=fmt.Errorf("usage report lacks execution identity");return}
 detail:=usage.EnsureTokenBreakdownForProvider(record.Detail,record.Provider,record.ExecutorType)
 b:=detail.TokenBreakdown
 r:=attemptRecord{account:AccountID(record.AuthID),known:b.Valid() && b.Quality==usage.TokenAccountingQualityComplete && b.TotalTokens>0,input:b.Input.TotalTokens,output:b.Output.TotalTokens,cached:b.Input.CacheReadTokens}
 key:=record.RequestID+"\x00"+record.Model
 if prior,ok:=a.records[key];ok {if prior!=r{a.err=ErrConflict};return};a.records[key]=r
}

func (a *AttemptUsage) Settlement(accountID string)(Settlement,error){
 a.mu.Lock();defer a.mu.Unlock();out:=Settlement{ID:a.id,AccountID:accountID,UsageKnown:len(a.records)>0}
 if a.err!=nil{return out,a.err}
 for _,r:=range a.records{if r.account!=accountID{return Settlement{ID:a.id,AccountID:accountID},ErrForbidden};if !r.known{out.UsageKnown=false};out.InputTokens+=r.input;out.OutputTokens+=r.output;out.CachedTokens+=r.cached}
 // A partly missing report is not an exact total. The ledger retains the bound.
 if !out.UsageKnown{out.InputTokens=0;out.OutputTokens=0;out.CachedTokens=0}
 return out,nil
}
