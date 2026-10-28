package neopool

import (
 "context"
 "database/sql"
 "encoding/json"
 "fmt"
 "math"
 "slices"
)

func (s *Store) Reserve(ctx context.Context,r Reservation) error {return s.ReserveAndAdvance(ctx,r,nil)}

// ReserveAndAdvance owns one transaction for the budget and durable draw cursor.
// The callback must use only the supplied transaction, with no external calls.
func (s *Store) ReserveAndAdvance(ctx context.Context,r Reservation,advance func(*sql.Tx)error) error {
 s.mu.Lock();defer s.mu.Unlock()
 tx,err:=s.db.BeginTx(ctx,nil);if err!=nil{return err};defer tx.Rollback()
 fresh:=false;err=s.reserveTx(ctx,tx,r,&fresh);if err!=nil{return err}
 if fresh && advance!=nil{if err=advance(tx);err!=nil{return err}}
 if err=tx.Commit();err!=nil{return err};if fresh{s.notifyLocked()};return nil
}

func (s *Store) reserveTx(ctx context.Context,tx *sql.Tx,r Reservation,fresh *bool) error {
 if validID(r.ID)!=nil || validID(r.RequestID)!=nil || r.UserID=="" || r.AccountID=="" || r.Tokens<0 || r.Tokens>1<<53{return fmt.Errorf("invalid attempt reservation")}
 if !r.BudgetFree && (r.Tokens==0 || r.Window=="" || r.VirtualModelID=="" || r.StageID=="" || math.IsNaN(r.PerAccount) || math.IsNaN(r.Cap) || r.PerAccount<=0 || r.PerAccount>1 || r.Cap<=0 || r.Cap>1){return fmt.Errorf("invalid budget reservation")}
 // Normalize pool order without modifying the caller's slice.
 r.AccountIDs=slices.Clone(r.AccountIDs);slices.Sort(r.AccountIDs);r.AccountIDs=slices.Compact(r.AccountIDs)
 if !r.BudgetFree && !slices.Contains(r.AccountIDs,r.AccountID){return ErrForbidden}
 encoded,err:=json.Marshal(r);if err!=nil{return err}
 if tx==nil{return fmt.Errorf("reservation transaction is required")}
 var spec,state string;err=tx.QueryRowContext(ctx,`SELECT request_spec,state FROM neo_pool_attempts WHERE id=?`,r.ID).Scan(&spec,&state)
 if err==nil {if spec!=string(encoded) || state=="released" {return ErrConflict};return nil};if err!=sql.ErrNoRows{return err}
 var engineID,ownerID string
 if err=tx.QueryRowContext(ctx,`SELECT engine_auth_id,COALESCE(owner_id,'') FROM neo_accounts WHERE id=?`,r.AccountID).Scan(&engineID,&ownerID);err!=nil{return ErrUnavailable};if ownerID==""{return ErrUnavailable}
 if r.ScopeOwnerID!="" && ownerID!=r.ScopeOwnerID{return ErrForbidden}
 var active int;if err=tx.QueryRowContext(ctx,`SELECT count(*) FROM neo_users WHERE id=? AND state='active'`,r.UserID).Scan(&active);err!=nil{return err};if active!=1{return ErrForbidden}
 // Recheck live engine health at the committing seam, not only in the resolver's snapshot.
 if s.manager==nil{return ErrUnavailable};a,ok:=s.manager.GetByID(engineID);if !ok || a.Disabled || a.Unavailable || a.Status!="active" || a.NextRetryAfter.After(s.now()) || a.Quota.NextRecoverAt.After(s.now()){return ErrUnavailable}
 now:=s.now().UnixMilli();var start,reset,capacity int64
 if !r.BudgetFree {
 var anchor int64;if err=tx.QueryRowContext(ctx,`SELECT capacity,anchor FROM neo_account_windows WHERE account_id=? AND kind=?`,r.AccountID,r.Window).Scan(&capacity,&anchor);err!=nil{return ErrUnavailable};start,reset,err=windowBounds(r.Window,anchor,now);if err!=nil{return err}
 var global int64;if err=tx.QueryRowContext(ctx,`SELECT COALESCE(sum(CASE WHEN state='reserved' THEN reserved ELSE tokens END),0) FROM neo_pool_attempts WHERE account_id=? AND created_at>=? AND created_at<? AND state!='released'`,r.AccountID,start,reset).Scan(&global);err!=nil{return err};if global>capacity-r.Tokens{return ErrBudget}
 var total,account float64
 // Each account has its own epoch and denominator. Old epochs never contribute
 // to a new one, and removed pool members do not debit the current stage cap.
 for _,id:=range r.AccountIDs {
 var capTokens,epochAnchor int64;err=tx.QueryRowContext(ctx,`SELECT capacity,anchor FROM neo_account_windows WHERE account_id=? AND kind=?`,id,r.Window).Scan(&capTokens,&epochAnchor);if err==sql.ErrNoRows{continue};if err!=nil{return err};epoch,_,errBounds:=windowBounds(r.Window,epochAnchor,now);if errBounds!=nil{continue}
 query:=`SELECT COALESCE(sum(CASE WHEN state='reserved' THEN reserved ELSE tokens END),0) FROM neo_pool_attempts WHERE virtual_model_id=? AND stage_id=? AND account_id=? AND window_kind=? AND starts_at=? AND state!='released'`;args:=[]any{r.VirtualModelID,r.StageID,id,r.Window,epoch};if !r.Shared{query+=` AND user_id=?`;args=append(args,r.UserID)}
 var tokens int64;if err=tx.QueryRowContext(ctx,query,args...).Scan(&tokens);err!=nil{return err};fraction:=float64(tokens)/float64(capTokens);total+=fraction;if id==r.AccountID{account=fraction}
 }
 debit:=float64(r.Tokens)/float64(capacity);if account+debit>r.PerAccount || total+debit>r.Cap{return ErrBudget}
 }
 _,err=tx.ExecContext(ctx,`INSERT INTO neo_pool_attempts(id,request_spec,request_id,user_id,virtual_model_id,stage_id,account_id,owner_id,window_kind,starts_at,resets_at,capacity,reserved,state,per_account,cap,shared,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,r.ID,string(encoded),r.RequestID,r.UserID,r.VirtualModelID,r.StageID,r.AccountID,ownerID,r.Window,start,reset,capacity,r.Tokens,"reserved",r.PerAccount,r.Cap,r.Shared,now)
 if err==nil{*fresh=true};return err
}

func (s *Store) Settle(ctx context.Context,v Settlement) error {
 if v.InputTokens<0 || v.OutputTokens<0 || v.CachedTokens<0 || v.CachedTokens>v.InputTokens || v.InputTokens>1<<53 || v.OutputTokens>1<<53 || v.InputTokens+v.OutputTokens>1<<53{return fmt.Errorf("invalid usage tokens")}
 if !v.UsageKnown && (v.InputTokens!=0 || v.OutputTokens!=0 || v.CachedTokens!=0){return fmt.Errorf("unknown usage cannot contain exact token counts")}
 encoded,err:=json.Marshal(v);if err!=nil{return err}
 s.mu.Lock();defer s.mu.Unlock();tx,err:=s.db.BeginTx(ctx,nil);if err!=nil{return err};defer tx.Rollback()
 var account,state string;var existing sql.NullString;var reserved int64
 if err=tx.QueryRowContext(ctx,`SELECT account_id,state,reserved,settlement_spec FROM neo_pool_attempts WHERE id=?`,v.ID).Scan(&account,&state,&reserved,&existing);err!=nil{return ErrUnavailable};if account!=v.AccountID{return ErrForbidden}
 if state=="settled"{if existing.String==string(encoded){return nil};return ErrConflict};if state!="reserved"{return ErrConflict}
 tokens:=v.InputTokens+v.OutputTokens;if !v.UsageKnown{tokens=reserved}
 _,err=tx.ExecContext(ctx,`UPDATE neo_pool_attempts SET state='settled',tokens=?,input_tokens=?,output_tokens=?,cached_tokens=?,estimated=?,settlement_spec=? WHERE id=?`,tokens,v.InputTokens,v.OutputTokens,v.CachedTokens,!v.UsageKnown,string(encoded),v.ID)
 if err!=nil{return err};if err=tx.Commit();err!=nil{return err};s.notifyLocked();return nil
}

// Release is only valid when the executor was never dispatched. Dispatched work
// with missing usage must be settled unknown, not made free by cancellation.
func (s *Store) Release(ctx context.Context,id string) error {
 s.mu.Lock();defer s.mu.Unlock();result,err:=s.db.ExecContext(ctx,`UPDATE neo_pool_attempts SET state='released' WHERE id=? AND state IN ('reserved','released')`,id);if err!=nil{return err};n,err:=result.RowsAffected();if err!=nil{return err};if n==0{return ErrConflict};s.notifyLocked();return nil
}

func (s *Store) Usage(ctx context.Context,userID string)([]Usage,error){
 s.mu.Lock();defer s.mu.Unlock();tx,err:=s.db.BeginTx(ctx,&sql.TxOptions{ReadOnly:true});if err!=nil{return nil,err};defer tx.Rollback()
 type meter struct{capacity,start,reset int64};meters:=map[string]meter{}
 rows,err:=tx.QueryContext(ctx,`SELECT account_id,kind,capacity,anchor FROM neo_account_windows`);if err!=nil{return nil,err}
 for rows.Next(){var id,kind string;var cap,anchor int64;if err=rows.Scan(&id,&kind,&cap,&anchor);err!=nil{rows.Close();return nil,err};start,reset,errBounds:=windowBounds(kind,anchor,s.now().UnixMilli());if errBounds==nil{meters[id+"\x00"+kind]=meter{cap,start,reset}}};if err=rows.Err();err!=nil{rows.Close();return nil,err};rows.Close()
 query:=`SELECT user_id,virtual_model_id,stage_id,account_id,window_kind,starts_at,resets_at,COALESCE(sum(tokens),0),COALESCE(sum(CASE WHEN state='reserved' THEN reserved ELSE 0 END),0) FROM neo_pool_attempts WHERE state!='released' AND window_kind!=''`;args:=[]any{};if userID!=""{query+=` AND user_id=?`;args=append(args,userID)};query+=` GROUP BY user_id,virtual_model_id,stage_id,account_id,window_kind,starts_at,resets_at ORDER BY user_id,virtual_model_id,stage_id,account_id`
 rows,err=tx.QueryContext(ctx,query,args...);if err!=nil{return nil,err};out:=[]Usage{}
 for rows.Next(){var u Usage;if err=rows.Scan(&u.UserID,&u.VirtualModelID,&u.StageID,&u.AccountID,&u.Window,&u.StartsAt,&u.ResetsAt,&u.Tokens,&u.Reserved);err!=nil{rows.Close();return nil,err};m,ok:=meters[u.AccountID+"\x00"+u.Window];if !ok || u.StartsAt!=m.start{continue};u.Fraction=float64(u.Tokens)/float64(m.capacity);out=append(out,u)}
 if err=rows.Err();err!=nil{rows.Close();return nil,err};rows.Close();if err=tx.Commit();err!=nil{return nil,err};return out,nil
}
