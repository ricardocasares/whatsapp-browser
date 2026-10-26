package memstore

import (
	"context"
	"testing"

	"google.golang.org/protobuf/proto"

	"go.mau.fi/whatsmeow/proto/waAdv"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
)

func TestDeviceRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := New(nil)
	dev := s.Device()
	if dev.ID != nil {
		t.Fatal("fresh device should have no ID")
	}
	jid := types.NewJID("15551234567", types.DefaultUserServer)
	jid.Device = 3
	dev.ID = &jid
	dev.LID = types.NewJID("111222333", types.HiddenUserServer)
	dev.PushName = "Rick"
	dev.Platform = "android"
	dev.Account = &waAdv.ADVSignedDeviceIdentity{Details: []byte{1, 2, 3}, AccountSignature: []byte{9}}
	if err := dev.Save(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.PutSession(ctx, "15551234567:0", []byte("sess")); err != nil {
		t.Fatal(err)
	}
	if err := s.PutIdentity(ctx, "15551234567:0", [32]byte{7}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetOrGenPreKeys(ctx, 5); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkPreKeysAsUploaded(ctx, 3); err != nil {
		t.Fatal(err)
	}
	if err := s.PutAppStateSyncKey(ctx, []byte{1}, store.AppStateSyncKey{Data: []byte("k"), Fingerprint: []byte("f"), Timestamp: 5}); err != nil {
		t.Fatal(err)
	}
	var hash [128]byte
	hash[0] = 42
	if err := s.PutAppStateVersion(ctx, "regular", 7, hash); err != nil {
		t.Fatal(err)
	}
	if err := s.PutLIDMapping(ctx, dev.LID, jid.ToNonAD()); err != nil {
		t.Fatal(err)
	}
	if err := s.PutContactName(ctx, jid.ToNonAD(), "Full", "First"); err != nil {
		t.Fatal(err)
	}

	data, err := s.Export()
	if err != nil {
		t.Fatal(err)
	}

	s2 := New(nil)
	if err := s2.Import(data); err != nil {
		t.Fatal(err)
	}
	if !s2.HasDevice() {
		t.Fatal("device missing after import")
	}
	dev2 := s2.Device()
	if dev2.ID == nil || *dev2.ID != jid {
		t.Fatalf("jid mismatch: %v", dev2.ID)
	}
	if dev2.LID != dev.LID || dev2.PushName != "Rick" || dev2.Platform != "android" {
		t.Fatalf("device fields mismatch: %+v", dev2)
	}
	if *dev2.NoiseKey.Priv != *dev.NoiseKey.Priv || *dev2.NoiseKey.Pub != *dev.NoiseKey.Pub {
		t.Fatal("noise key mismatch")
	}
	if *dev2.IdentityKey.Priv != *dev.IdentityKey.Priv {
		t.Fatal("identity key mismatch")
	}
	if *dev2.SignedPreKey.Priv != *dev.SignedPreKey.Priv || dev2.SignedPreKey.KeyID != dev.SignedPreKey.KeyID || *dev2.SignedPreKey.Signature != *dev.SignedPreKey.Signature {
		t.Fatal("signed prekey mismatch")
	}
	if dev2.RegistrationID != dev.RegistrationID || string(dev2.AdvSecretKey) != string(dev.AdvSecretKey) {
		t.Fatal("registration/adv mismatch")
	}
	if !proto.Equal(dev2.Account, dev.Account) {
		t.Fatal("account mismatch")
	}
	if sess, _ := s2.GetSession(ctx, "15551234567:0"); string(sess) != "sess" {
		t.Fatal("session missing")
	}
	if ok, _ := s2.IsTrustedIdentity(ctx, "15551234567:0", [32]byte{7}); !ok {
		t.Fatal("identity should be trusted")
	}
	if ok, _ := s2.IsTrustedIdentity(ctx, "15551234567:0", [32]byte{8}); ok {
		t.Fatal("wrong identity should not be trusted")
	}
	if n, _ := s2.UploadedPreKeyCount(ctx); n != 3 {
		t.Fatalf("uploaded prekeys = %d, want 3", n)
	}
	more, _ := s2.GetOrGenPreKeys(ctx, 4)
	if len(more) != 4 || more[0].KeyID != 4 || more[3].KeyID != 7 {
		t.Fatalf("unexpected prekey ids: %d..%d", more[0].KeyID, more[len(more)-1].KeyID)
	}
	if k, _ := s2.GetAppStateSyncKey(ctx, []byte{1}); k == nil || string(k.Data) != "k" {
		t.Fatal("app state key missing")
	}
	if id, _ := s2.GetLatestAppStateSyncKeyID(ctx); len(id) != 1 || id[0] != 1 {
		t.Fatal("latest app state key id wrong")
	}
	if v, h, _ := s2.GetAppStateVersion(ctx, "regular"); v != 7 || h[0] != 42 {
		t.Fatal("app state version wrong")
	}
	if pn, _ := s2.GetPNForLID(ctx, dev.LID); pn.User != jid.User {
		t.Fatalf("lid mapping wrong: %v", pn)
	}
	if lid, _ := s2.GetLIDForPN(ctx, jid.ToNonAD()); lid.User != dev.LID.User {
		t.Fatalf("pn mapping wrong: %v", lid)
	}
	if c, _ := s2.GetContact(ctx, jid); !c.Found || c.FullName != "Full" {
		t.Fatal("contact missing")
	}

	// Delete resets everything.
	if err := dev2.Delete(ctx); err != nil {
		t.Fatal(err)
	}
	if s2.HasDevice() {
		t.Fatal("device should be gone")
	}
	if s2.Device().ID != nil {
		t.Fatal("fresh device expected after delete")
	}
}

func TestMigratePNToLID(t *testing.T) {
	ctx := context.Background()
	s := New(nil)
	pn := types.NewJID("1555", types.DefaultUserServer)
	lid := types.NewJID("999", types.HiddenUserServer)
	s.PutSession(ctx, "1555:0", []byte("a"))
	s.PutSession(ctx, "1555:2", []byte("b"))
	s.PutIdentity(ctx, "1555:0", [32]byte{1})
	s.PutSenderKey(ctx, "g@g.us", "1555:0", []byte("sk"))
	if err := s.MigratePNToLID(ctx, pn, lid); err != nil {
		t.Fatal(err)
	}
	if v, _ := s.GetSession(ctx, lid.SignalAddressUser()+":2"); string(v) != "b" {
		t.Fatal("session not migrated")
	}
	if v, _ := s.GetSession(ctx, "1555:0"); v != nil {
		t.Fatal("old session should be gone")
	}
	if v, _ := s.GetSenderKey(ctx, "g@g.us", lid.SignalAddressUser()+":0"); string(v) != "sk" {
		t.Fatal("sender key not migrated")
	}
}
