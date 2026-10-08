package repository

import (
	"database/sql"
	"errors"
	"strings"
	"time"
)

// RDPSecurityPage returns a stable, server-side paginated query. Counts are
// computed across all retained public-RDP events, not only the latest 100.
type RDPSecurityPage[T any] struct {
	Items []T `json:"items"`
	Page int `json:"page"`
	PageSize int `json:"pageSize"`
	Total int64 `json:"total"`
}

type RDPSourceGroup struct {
	SourceIP string `json:"sourceIp"`
	Targets []string `json:"targets"`
	ConnectionCount int64 `json:"connectionCount"`
	ForwardedCount int64 `json:"forwardedCount"`
	RejectedCount int64 `json:"rejectedCount"`
	BytesUp int64 `json:"bytesUp"`
	BytesDown int64 `json:"bytesDown"`
	FirstSeen time.Time `json:"firstSeen"`
	LastSeen time.Time `json:"lastSeen"`
}

func validateRDPLogPage(page, size int) (int, int, error) {
	if page < 1 || page > 100000 || size < 1 || size > 100 {
		return 0,0,errors.New("page must be 1-100000 and pageSize must be 1-100")
	}
	return page, size, nil
}

// ListRDPSecuritySourceGroups groups across the full log table by public IP.
// Uses a LEFT JOIN to display current device names; deleted devices fall
// back to their stored device identifiers.
func (db *DB) ListRDPSecuritySourceGroups(search string, page, size int) (*RDPSecurityPage[RDPSourceGroup],error) {
	if _, _, err:=validateRDPLogPage(page,size);err!=nil { return nil,err }
	pattern := strings.TrimSpace(search)
	countQuery := "SELECT COUNT(DISTINCT source_ip) FROM rdp_security_logs WHERE instr(source_ip, ?) > 0"
	var total int64
	if err:=db.QueryRow(countQuery,pattern).Scan(&total);err!=nil { return nil,err }
	rows,err:=db.Query(`SELECT l.source_ip,
		COALESCE(GROUP_CONCAT(DISTINCT COALESCE(NULLIF(d.name,''),NULLIF(l.target_device_id,''),'未知设备')),''),
		COUNT(*), SUM(CASE WHEN l.result = 'FORWARDED' THEN 1 ELSE 0 END),
		SUM(CASE WHEN l.result = 'REJECTED' THEN 1 ELSE 0 END),
		COALESCE(SUM(l.bytes_up),0), COALESCE(SUM(l.bytes_down),0),
		MIN(l.started_at),MAX(l.started_at)
		FROM rdp_security_logs l
		LEFT JOIN devices d ON d.id = l.target_device_id
		WHERE instr(l.source_ip, ?) > 0
		GROUP BY l.source_ip
		ORDER BY MAX(l.started_at) DESC,l.source_ip ASC
		LIMIT ? OFFSET ?`,pattern,size,(page-1)*size)
	if err!=nil { return nil,err }
	defer rows.Close()
	output:=&RDPSecurityPage[RDPSourceGroup]{Items:make([]RDPSourceGroup,0),Page:page,PageSize:size,Total:total}
	for rows.Next() {
		var item RDPSourceGroup
		var targets string
		if err:=rows.Scan(&item.SourceIP,&targets,&item.ConnectionCount,&item.ForwardedCount,
			&item.RejectedCount,&item.BytesUp,&item.BytesDown,&item.FirstSeen,&item.LastSeen);err!=nil {return nil,err}
		if targets!="" { item.Targets=strings.Split(targets,",") } else { item.Targets=[]string{} }
		output.Items=append(output.Items,item)
	}
	return output,rows.Err()
}

// ListRDPSecurityLogPage returns source-specific connection details with the
// real device name, connection times and outcome. If sourceIP is empty, all
// records are paginated (for API clients, not the grouped admin default UI).
func (db *DB) ListRDPSecurityLogPage(sourceIP string, page, size int) (*RDPSecurityPage[RDPSecurityLog],error) {
	if _,_,err:=validateRDPLogPage(page,size);err!=nil {return nil,err}
	where:=""
	args:=make([]any,0,3)
	if sourceIP!="" { where=" WHERE l.source_ip = ?"; args=append(args,sourceIP) }
	var total int64
	countQuery:="SELECT COUNT(*) FROM rdp_security_logs l"+where
	if err:=db.QueryRow(countQuery,args...).Scan(&total);err!=nil {return nil,err}
	query:=`SELECT l.id,l.ingress_id,l.target_device_id,
		COALESCE(NULLIF(d.name,''),NULLIF(l.target_device_id,''),'未知设备') AS target_name,
		l.source_ip,l.transport,l.result,l.reason,l.started_at,l.ended_at,l.bytes_up,l.bytes_down
		FROM rdp_security_logs l LEFT JOIN devices d ON d.id=l.target_device_id`+where+
		" ORDER BY l.started_at DESC,l.id DESC LIMIT ? OFFSET ?"
	args=append(args,size,(page-1)*size)
	rows,err:=db.Query(query,args...)
	if err!=nil {return nil,err}
	defer rows.Close()
	output:=&RDPSecurityPage[RDPSecurityLog]{Items:make([]RDPSecurityLog,0),Page:page,PageSize:size,Total:total}
	for rows.Next() {
		var item RDPSecurityLog
		if err:=rows.Scan(&item.ID,&item.IngressID,&item.TargetDeviceID,&item.TargetName,&item.SourceIP,&item.Transport,
			&item.Result,&item.Reason,&item.StartedAt,&item.EndedAt,&item.BytesUp,&item.BytesDown);err!=nil {return nil,err}
		output.Items=append(output.Items,item)
	}
	return output,rows.Err()
}

// DeleteRDPSecurityLogs clears connection audit only; IP bans, allowlists,
// security rules and unrelated connection_audit are intentionally retained.
func (db *DB) DeleteRDPSecurityLogs(sourceIP string) (int64,error) {
	var result sql.Result
	var err error
	if sourceIP!="" { result,err=db.Exec("DELETE FROM rdp_security_logs WHERE source_ip = ?",sourceIP) } else {
		result,err=db.Exec("DELETE FROM rdp_security_logs")
	}
	if err!=nil {return 0,err}
	return result.RowsAffected()
}
