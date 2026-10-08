package repository

import (
	"time"

	"github.com/google/uuid"
)

// RDPAuthFailure is an observed Windows Security 4625, not an inferred
// disconnect or a tunnel error. Source IP is assigned by Server correlation.
type RDPAuthFailure struct {
	ID string `json:"id"`
	TargetDeviceID string `json:"targetDeviceId"`
	EventRecordID uint64 `json:"eventRecordId"`
	SourcePort int `json:"sourcePort"`
	LogonType int `json:"logonType"`
	Username string `json:"username"`
	Status string `json:"status"`
	SubStatus string `json:"subStatus"`
	OccurredAt time.Time `json:"occurredAt"`
	ReceivedAt time.Time `json:"receivedAt"`
	SourceIP string `json:"sourceIp"`
	IngressID string `json:"ingressId"`
	ConnectionID string `json:"connectionId"`
	Correlated bool `json:"correlated"`
}

// InsertRDPAuthFailure returns false for previously recorded Windows event IDs,
// preventing replayed events from incrementing login-failure ban counters.
func (db *DB) InsertRDPAuthFailure(entry *RDPAuthFailure) (bool, error) {
	if entry.ID == "" { entry.ID = uuid.NewString() }
	if entry.ReceivedAt.IsZero() { entry.ReceivedAt = time.Now().UTC() }
	res, err := db.Exec(`INSERT OR IGNORE INTO rdp_auth_failures
	(id,target_device_id,event_record_id,source_port,logon_type,username,status,sub_status,occurred_at,received_at,source_ip,ingress_id,connection_id,correlated)
	VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		entry.ID, entry.TargetDeviceID, entry.EventRecordID, entry.SourcePort, entry.LogonType,
		entry.Username, entry.Status, entry.SubStatus, entry.OccurredAt.UTC(), entry.ReceivedAt.UTC(),
		entry.SourceIP, entry.IngressID, entry.ConnectionID, entry.Correlated)
	if err != nil { return false, err }
	n, err := res.RowsAffected()
	return n > 0, err
}

func (db *DB) ListRDPAuthFailures(ip, ingressID string, limit int) ([]RDPAuthFailure,error) {
	if limit < 1 { limit = 100 }
	if limit > 500 { limit = 500 }
	query := `SELECT id,target_device_id,event_record_id,source_port,logon_type,username,status,sub_status,
		occurred_at,received_at,source_ip,ingress_id,connection_id,correlated FROM rdp_auth_failures WHERE 1=1`
	args := make([]any,0,3)
	if ip != "" { query += " AND source_ip = ?"; args=append(args,ip) }
	if ingressID != "" { query += " AND ingress_id = ?"; args=append(args,ingressID) }
	query += " ORDER BY occurred_at DESC LIMIT ?"
	args=append(args,limit)
	rows,err:=db.Query(query,args...)
	if err != nil { return nil,err }
	defer rows.Close()
	out:=make([]RDPAuthFailure,0)
	for rows.Next() {
		var item RDPAuthFailure
		if err:=rows.Scan(&item.ID,&item.TargetDeviceID,&item.EventRecordID,&item.SourcePort,&item.LogonType,
			&item.Username,&item.Status,&item.SubStatus,&item.OccurredAt,&item.ReceivedAt,
			&item.SourceIP,&item.IngressID,&item.ConnectionID,&item.Correlated); err!=nil { return nil,err }
		out=append(out,item)
	}
	return out,rows.Err()
}
