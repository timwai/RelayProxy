package browsersync

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestBrowserApprovalResolvesPublicIdentityAndPreservesRequestedBinding(t *testing.T) {
	const publicID = "a1b2c3d4e5f6g7h8"
	const otherPublicID = "b1c2d3e4f5g6h7i8"
	for _, extension := range []bool{false, true} {
		path := "legacy"
		if extension {
			path = "extension"
		}
		for _, tc := range []struct {
			name, requested, approval string
			disabled, denied          bool
		}{
			{name: "public ID", requested: publicID, approval: publicID},
			{name: "internal approval", requested: publicID, approval: "id_1"},
			{name: "legacy request", requested: "id_1", approval: publicID},
			{name: "legacy IDs", requested: "id_1", approval: "id_1"},
			{name: "uppercase request", requested: strings.ToUpper(publicID), approval: publicID},
			{name: "uppercase approval", requested: publicID, approval: strings.ToUpper(publicID)},
			{name: "old request without identity", approval: publicID},
			{name: "different public identity", requested: otherPublicID, approval: publicID, denied: true},
			{name: "different internal identity", requested: "id_other", approval: publicID, denied: true},
			{name: "unknown requested identity", requested: "unknown", approval: publicID, denied: true},
			{name: "unknown approval identity", requested: publicID, approval: "unknown", denied: true},
			{name: "disabled public identity", requested: publicID, approval: publicID, disabled: true, denied: true},
			{name: "disabled internal identity", requested: publicID, approval: "id_1", disabled: true, denied: true},
		} {
			t.Run(path+"/"+tc.name, func(t *testing.T) {
				store := testBrowserStore(t)
				ctx := context.Background()
				if _, err := store.db.Exec(`INSERT INTO identities(id,short_id,status) VALUES('id_other',?,'active')`, otherPublicID); err != nil {
					t.Fatal(err)
				}
				device := unifiedRegistration(t)
				device.RequestedIdentityID = tc.requested
				if extension {
					if _, _, err := store.EnrollExtensionDevice(ctx, unifiedExtensionID, device); err != nil {
						t.Fatal(err)
					}
				} else if _, err := store.Register(ctx, device); err != nil {
					t.Fatal(err)
				}
				if tc.disabled {
					if _, err := store.db.Exec(`UPDATE identities SET status='disabled' WHERE id='id_1'`); err != nil {
						t.Fatal(err)
					}
				}
				approve := store.Approve
				if extension {
					approve = store.ApproveEnrollment
				}
				err := approve(ctx, device.ID, tc.approval, true, false)
				if (err != nil) != tc.denied {
					t.Fatalf("approval error = %v, want denied = %v", err, tc.denied)
				}
				saved, err := store.Device(ctx, device.ID)
				if err != nil {
					t.Fatal(err)
				}
				if saved.RequestedIdentityID != tc.requested {
					t.Fatalf("approval rewrote the original identity request: %+v", saved)
				}
				if tc.denied {
					if saved.State != "pending" || saved.IdentityID != "" || saved.Send || saved.Receive {
						t.Fatalf("rejected approval granted privileges: %+v", saved)
					}
				} else if saved.State != "approved" || saved.IdentityID != "id_1" || !saved.Send || saved.Receive {
					t.Fatalf("approval did not store the canonical identity and permissions: %+v", saved)
				}
			})
		}
	}
}

func TestBrowserPublicIdentityApprovalUsesExistingQuota(t *testing.T) {
	for _, extension := range []bool{false, true} {
		path := "legacy"
		if extension {
			path = "extension"
		}
		t.Run(path, func(t *testing.T) {
			store := testBrowserStore(t)
			ctx := context.Background()
			seed := seedApprovedBrowsers(t, store, "id_1", maxApprovedBrowserDevicesPerIdentity)
			device := unifiedRegistration(t)
			device.RequestedIdentityID = "a1b2c3d4e5f6g7h8"
			if extension {
				if _, _, err := store.EnrollExtensionDevice(ctx, unifiedExtensionID, device); err != nil {
					t.Fatal(err)
				}
			} else if _, err := store.Register(ctx, device); err != nil {
				t.Fatal(err)
			}
			approve := store.Approve
			if extension {
				approve = store.ApproveEnrollment
			}
			if err := approve(ctx, device.ID, device.RequestedIdentityID, true, false); !errors.Is(err, ErrBrowserIdentityQuota) {
				t.Fatalf("public ID bypassed canonical identity quota: %v", err)
			}
			if err := store.Revoke(ctx, seed[0]); err != nil {
				t.Fatal(err)
			}
			if err := approve(ctx, device.ID, device.RequestedIdentityID, true, false); err != nil {
				t.Fatalf("public ID failed after releasing a quota slot: %v", err)
			}
		})
	}
}
