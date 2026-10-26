// Package memstore is an in-memory implementation of whatsmeow's store
// interfaces. The whole state can be serialized to JSON and restored later,
// which lets the browser build persist it in IndexedDB.
package memstore

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	mathRand "math/rand"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"go.mau.fi/util/random"
	"google.golang.org/protobuf/proto"

	"go.mau.fi/whatsmeow/proto/waAdv"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/util/keys"
	waLog "go.mau.fi/whatsmeow/util/log"
)

// Store holds all whatsmeow state in memory. OnChange is called (debounced by
// the caller if needed) after every mutation so the state can be persisted.
type Store struct {
	mu       sync.Mutex
	OnChange func()
	log      waLog.Logger

	identities   map[string][]byte
	sessions     map[string][]byte
	preKeys      map[uint32]*preKeyEntry
	senderKeys   map[string][]byte                 // group + "|" + user
	appStateKeys map[string]*store.AppStateSyncKey // base64 id
	appStateVers map[string]*appStateVersion
	appStateMACs map[string][]byte // name + "|" + base64 indexMAC
	contacts     map[string]*types.ContactInfo
	chatSettings map[string]*chatSettings
	msgSecrets   map[string]*msgSecret // chat|sender|id
	privTokens   map[string]*store.PrivacyToken
	nctSalt      []byte
	bufEvents    map[string]*store.BufferedEvent // base64 hash
	outEvents    map[string]*outgoingEvent       // chat|id
	lidToPN      map[string]string
	pnToLID      map[string]string
	migratedPNs  map[string]bool

	device *deviceData
}

type preKeyEntry struct {
	Key      []byte `json:"key"`
	Uploaded bool   `json:"uploaded"`
}

type appStateVersion struct {
	Version uint64 `json:"version"`
	Hash    []byte `json:"hash"`
}

type chatSettings struct {
	MutedUntil int64  `json:"muted_until"`
	Pinned     bool   `json:"pinned"`
	Archived   bool   `json:"archived"`
	WASARoot   string `json:"wasa_root"`
}

type msgSecret struct {
	Secret []byte `json:"secret"`
	Sender string `json:"sender"`
}

type outgoingEvent struct {
	Format    string    `json:"format"`
	Plaintext []byte    `json:"plaintext"`
	Time      time.Time `json:"time"`
}

type deviceData struct {
	NoiseKey              []byte `json:"noise_key"`
	IdentityKey           []byte `json:"identity_key"`
	SignedPreKey          []byte `json:"signed_pre_key"`
	SignedPreKeyID        uint32 `json:"signed_pre_key_id"`
	SignedPreKeySig       []byte `json:"signed_pre_key_sig"`
	RegistrationID        uint32 `json:"registration_id"`
	AdvSecretKey          []byte `json:"adv_secret_key"`
	ID                    string `json:"id"`
	LID                   string `json:"lid"`
	Account               []byte `json:"account"`
	Platform              string `json:"platform"`
	BusinessName          string `json:"business_name"`
	PushName              string `json:"push_name"`
	LIDMigrationTimestamp int64  `json:"lid_migration_ts"`
	FacebookUUID          string `json:"facebook_uuid"`
}

type snapshot struct {
	Version      int                               `json:"version"`
	Device       *deviceData                       `json:"device"`
	Identities   map[string][]byte                 `json:"identities"`
	Sessions     map[string][]byte                 `json:"sessions"`
	PreKeys      map[uint32]*preKeyEntry           `json:"pre_keys"`
	SenderKeys   map[string][]byte                 `json:"sender_keys"`
	AppStateKeys map[string]*store.AppStateSyncKey `json:"app_state_keys"`
	AppStateVers map[string]*appStateVersion       `json:"app_state_versions"`
	AppStateMACs map[string][]byte                 `json:"app_state_macs"`
	Contacts     map[string]*types.ContactInfo     `json:"contacts"`
	ChatSettings map[string]*chatSettings          `json:"chat_settings"`
	MsgSecrets   map[string]*msgSecret             `json:"msg_secrets"`
	PrivTokens   map[string]*store.PrivacyToken    `json:"privacy_tokens"`
	NCTSalt      []byte                            `json:"nct_salt"`
	OutEvents    map[string]*outgoingEvent         `json:"outgoing_events"`
	LIDToPN      map[string]string                 `json:"lid_to_pn"`
}

func New(log waLog.Logger) *Store {
	if log == nil {
		log = waLog.Noop
	}
	s := &Store{log: log}
	s.reset()
	return s
}

func (s *Store) reset() {
	s.identities = map[string][]byte{}
	s.sessions = map[string][]byte{}
	s.preKeys = map[uint32]*preKeyEntry{}
	s.senderKeys = map[string][]byte{}
	s.appStateKeys = map[string]*store.AppStateSyncKey{}
	s.appStateVers = map[string]*appStateVersion{}
	s.appStateMACs = map[string][]byte{}
	s.contacts = map[string]*types.ContactInfo{}
	s.chatSettings = map[string]*chatSettings{}
	s.msgSecrets = map[string]*msgSecret{}
	s.privTokens = map[string]*store.PrivacyToken{}
	s.nctSalt = nil
	s.bufEvents = map[string]*store.BufferedEvent{}
	s.outEvents = map[string]*outgoingEvent{}
	s.lidToPN = map[string]string{}
	s.pnToLID = map[string]string{}
	s.migratedPNs = map[string]bool{}
	s.device = nil
}

func (s *Store) changed() {
	if s.OnChange != nil {
		s.OnChange()
	}
}

// Export serializes the full state to JSON.
func (s *Store) Export() ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	snap := snapshot{
		Version:      1,
		Device:       s.device,
		Identities:   s.identities,
		Sessions:     s.sessions,
		PreKeys:      s.preKeys,
		SenderKeys:   s.senderKeys,
		AppStateKeys: s.appStateKeys,
		AppStateVers: s.appStateVers,
		AppStateMACs: s.appStateMACs,
		Contacts:     s.contacts,
		ChatSettings: s.chatSettings,
		MsgSecrets:   s.msgSecrets,
		PrivTokens:   s.privTokens,
		NCTSalt:      s.nctSalt,
		OutEvents:    s.outEvents,
		LIDToPN:      s.lidToPN,
	}
	return json.Marshal(&snap)
}

// Import restores a state previously produced by Export.
func (s *Store) Import(data []byte) error {
	var snap snapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reset()
	s.device = snap.Device
	fill := func(dst, src any) {}
	_ = fill
	if snap.Identities != nil {
		s.identities = snap.Identities
	}
	if snap.Sessions != nil {
		s.sessions = snap.Sessions
	}
	if snap.PreKeys != nil {
		s.preKeys = snap.PreKeys
	}
	if snap.SenderKeys != nil {
		s.senderKeys = snap.SenderKeys
	}
	if snap.AppStateKeys != nil {
		s.appStateKeys = snap.AppStateKeys
	}
	if snap.AppStateVers != nil {
		s.appStateVers = snap.AppStateVers
	}
	if snap.AppStateMACs != nil {
		s.appStateMACs = snap.AppStateMACs
	}
	if snap.Contacts != nil {
		s.contacts = snap.Contacts
	}
	if snap.ChatSettings != nil {
		s.chatSettings = snap.ChatSettings
	}
	if snap.MsgSecrets != nil {
		s.msgSecrets = snap.MsgSecrets
	}
	if snap.PrivTokens != nil {
		s.privTokens = snap.PrivTokens
	}
	s.nctSalt = snap.NCTSalt
	if snap.OutEvents != nil {
		s.outEvents = snap.OutEvents
	}
	if snap.LIDToPN != nil {
		s.lidToPN = snap.LIDToPN
		for lid, pn := range s.lidToPN {
			s.pnToLID[pn] = lid
		}
	}
	return nil
}

// HasDevice reports whether a paired device is stored.
func (s *Store) HasDevice() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.device != nil && s.device.ID != ""
}

// Device returns the stored device, or a brand-new unpaired one.
func (s *Store) Device() *store.Device {
	s.mu.Lock()
	d := s.device
	s.mu.Unlock()
	dev := &store.Device{Log: s.log, Container: s}
	dev.SetAllStores(s)
	dev.LIDs = s
	if d == nil || d.ID == "" {
		dev.NoiseKey = keys.NewKeyPair()
		dev.IdentityKey = keys.NewKeyPair()
		dev.RegistrationID = mathRand.Uint32()
		dev.AdvSecretKey = random.Bytes(32)
		dev.SignedPreKey = dev.IdentityKey.CreateSignedPreKey(1)
		return dev
	}
	dev.NoiseKey = keys.NewKeyPairFromPrivateKey(*(*[32]byte)(d.NoiseKey))
	dev.IdentityKey = keys.NewKeyPairFromPrivateKey(*(*[32]byte)(d.IdentityKey))
	dev.SignedPreKey = &keys.PreKey{
		KeyPair:   *keys.NewKeyPairFromPrivateKey(*(*[32]byte)(d.SignedPreKey)),
		KeyID:     d.SignedPreKeyID,
		Signature: (*[64]byte)(d.SignedPreKeySig),
	}
	dev.RegistrationID = d.RegistrationID
	dev.AdvSecretKey = d.AdvSecretKey
	jid, err := types.ParseJID(d.ID)
	if err == nil {
		dev.ID = &jid
	}
	if d.LID != "" {
		dev.LID, _ = types.ParseJID(d.LID)
	}
	if len(d.Account) > 0 {
		dev.Account = &waAdv.ADVSignedDeviceIdentity{}
		if err := proto.Unmarshal(d.Account, dev.Account); err != nil {
			s.log.Warnf("Failed to unmarshal account: %v", err)
			dev.Account = nil
		}
	}
	dev.Platform = d.Platform
	dev.BusinessName = d.BusinessName
	dev.PushName = d.PushName
	dev.LIDMigrationTimestamp = d.LIDMigrationTimestamp
	if d.FacebookUUID != "" {
		dev.FacebookUUID, _ = uuid.Parse(d.FacebookUUID)
	}
	dev.Initialized = true
	return dev
}

// ---- DeviceContainer ----

func (s *Store) PutDevice(ctx context.Context, dev *store.Device) error {
	if dev.ID == nil {
		return errors.New("device JID must be known before saving")
	}
	d := &deviceData{
		NoiseKey:              dev.NoiseKey.Priv[:],
		IdentityKey:           dev.IdentityKey.Priv[:],
		SignedPreKey:          dev.SignedPreKey.Priv[:],
		SignedPreKeyID:        dev.SignedPreKey.KeyID,
		SignedPreKeySig:       dev.SignedPreKey.Signature[:],
		RegistrationID:        dev.RegistrationID,
		AdvSecretKey:          dev.AdvSecretKey,
		ID:                    dev.ID.String(),
		LID:                   dev.LID.String(),
		Platform:              dev.Platform,
		BusinessName:          dev.BusinessName,
		PushName:              dev.PushName,
		LIDMigrationTimestamp: dev.LIDMigrationTimestamp,
		FacebookUUID:          dev.FacebookUUID.String(),
	}
	if dev.Account != nil {
		b, err := proto.Marshal(dev.Account)
		if err != nil {
			return err
		}
		d.Account = b
	}
	s.mu.Lock()
	s.device = d
	dev.Initialized = true
	s.mu.Unlock()
	s.changed()
	return nil
}

func (s *Store) DeleteDevice(ctx context.Context, dev *store.Device) error {
	s.mu.Lock()
	s.reset()
	s.mu.Unlock()
	s.changed()
	return nil
}

// ---- IdentityStore ----

func (s *Store) PutIdentity(ctx context.Context, address string, key [32]byte) error {
	s.mu.Lock()
	s.identities[address] = key[:]
	s.mu.Unlock()
	s.changed()
	return nil
}

func (s *Store) DeleteAllIdentities(ctx context.Context, phone string) error {
	s.mu.Lock()
	for k := range s.identities {
		if strings.HasPrefix(k, phone+":") {
			delete(s.identities, k)
		}
	}
	s.mu.Unlock()
	s.changed()
	return nil
}

func (s *Store) DeleteIdentity(ctx context.Context, address string) error {
	s.mu.Lock()
	delete(s.identities, address)
	s.mu.Unlock()
	s.changed()
	return nil
}

func (s *Store) IsTrustedIdentity(ctx context.Context, address string, key [32]byte) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	existing, ok := s.identities[address]
	if !ok {
		return true, nil
	}
	if len(existing) != 32 {
		return false, errors.New("invalid identity length")
	}
	return *(*[32]byte)(existing) == key, nil
}

// ---- SessionStore ----

func (s *Store) GetSession(ctx context.Context, address string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessions[address], nil
}

func (s *Store) HasSession(ctx context.Context, address string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.sessions[address]
	return ok, nil
}

func (s *Store) GetManySessions(ctx context.Context, addresses []string) (map[string][]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string][]byte, len(addresses))
	for _, a := range addresses {
		if v, ok := s.sessions[a]; ok {
			out[a] = v
		}
	}
	return out, nil
}

func (s *Store) PutSession(ctx context.Context, address string, session []byte) error {
	s.mu.Lock()
	s.sessions[address] = session
	s.mu.Unlock()
	s.changed()
	return nil
}

func (s *Store) PutManySessions(ctx context.Context, sessions map[string][]byte) error {
	s.mu.Lock()
	for k, v := range sessions {
		s.sessions[k] = v
	}
	s.mu.Unlock()
	s.changed()
	return nil
}

func (s *Store) DeleteAllSessions(ctx context.Context, phone string) error {
	s.mu.Lock()
	for k := range s.sessions {
		if strings.HasPrefix(k, phone+":") {
			delete(s.sessions, k)
		}
	}
	s.mu.Unlock()
	s.changed()
	return nil
}

func (s *Store) DeleteSession(ctx context.Context, address string) error {
	s.mu.Lock()
	delete(s.sessions, address)
	s.mu.Unlock()
	s.changed()
	return nil
}

func (s *Store) MigratePNToLID(ctx context.Context, pn, lid types.JID) error {
	pnSignal := pn.SignalAddressUser()
	lidSignal := lid.SignalAddressUser()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.migratedPNs[pnSignal] {
		return nil
	}
	s.migratedPNs[pnSignal] = true
	migrate := func(m map[string][]byte) {
		for k, v := range m {
			if strings.HasPrefix(k, pnSignal+":") {
				newKey := lidSignal + k[len(pnSignal):]
				if _, exists := m[newKey]; !exists {
					m[newKey] = v
				}
				delete(m, k)
			}
		}
	}
	migrate(s.sessions)
	migrate(s.identities)
	for k, v := range s.senderKeys {
		// key is group|user
		parts := strings.SplitN(k, "|", 2)
		if len(parts) == 2 && strings.HasPrefix(parts[1], pnSignal+":") {
			newKey := parts[0] + "|" + lidSignal + parts[1][len(pnSignal):]
			if _, exists := s.senderKeys[newKey]; !exists {
				s.senderKeys[newKey] = v
			}
			delete(s.senderKeys, k)
		}
	}
	go s.changed()
	return nil
}

// ---- PreKeyStore ----

func (s *Store) nextPreKeyID() uint32 {
	var max uint32
	for id := range s.preKeys {
		if id > max {
			max = id
		}
	}
	return max + 1
}

func (s *Store) preKeyFromEntry(id uint32, e *preKeyEntry) *keys.PreKey {
	return &keys.PreKey{
		KeyPair: *keys.NewKeyPairFromPrivateKey(*(*[32]byte)(e.Key)),
		KeyID:   id,
	}
}

func (s *Store) GetOrGenPreKeys(ctx context.Context, count uint32) ([]*keys.PreKey, error) {
	s.mu.Lock()
	var ids []int
	for id, e := range s.preKeys {
		if !e.Uploaded {
			ids = append(ids, int(id))
		}
	}
	sort.Ints(ids)
	var out []*keys.PreKey
	for _, id := range ids {
		if uint32(len(out)) >= count {
			break
		}
		out = append(out, s.preKeyFromEntry(uint32(id), s.preKeys[uint32(id)]))
	}
	generated := false
	nextID := s.nextPreKeyID()
	for uint32(len(out)) < count {
		k := keys.NewPreKey(nextID)
		s.preKeys[nextID] = &preKeyEntry{Key: k.Priv[:], Uploaded: false}
		out = append(out, k)
		nextID++
		generated = true
	}
	s.mu.Unlock()
	if generated {
		s.changed()
	}
	return out, nil
}

func (s *Store) GenOnePreKey(ctx context.Context) (*keys.PreKey, error) {
	s.mu.Lock()
	id := s.nextPreKeyID()
	k := keys.NewPreKey(id)
	s.preKeys[id] = &preKeyEntry{Key: k.Priv[:], Uploaded: true}
	s.mu.Unlock()
	s.changed()
	return k, nil
}

func (s *Store) GetPreKey(ctx context.Context, id uint32) (*keys.PreKey, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.preKeys[id]
	if !ok {
		return nil, nil
	}
	return s.preKeyFromEntry(id, e), nil
}

func (s *Store) RemovePreKey(ctx context.Context, id uint32) error {
	s.mu.Lock()
	delete(s.preKeys, id)
	s.mu.Unlock()
	s.changed()
	return nil
}

func (s *Store) MarkPreKeysAsUploaded(ctx context.Context, upToID uint32) error {
	s.mu.Lock()
	for id, e := range s.preKeys {
		if id <= upToID {
			e.Uploaded = true
		}
	}
	s.mu.Unlock()
	s.changed()
	return nil
}

func (s *Store) UploadedPreKeyCount(ctx context.Context) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, e := range s.preKeys {
		if e.Uploaded {
			n++
		}
	}
	return n, nil
}

// ---- SenderKeyStore ----

func (s *Store) PutSenderKey(ctx context.Context, group, user string, session []byte) error {
	s.mu.Lock()
	s.senderKeys[group+"|"+user] = session
	s.mu.Unlock()
	s.changed()
	return nil
}

func (s *Store) GetSenderKey(ctx context.Context, group, user string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.senderKeys[group+"|"+user], nil
}

// ---- AppStateSyncKeyStore ----

func b64(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

func (s *Store) PutAppStateSyncKey(ctx context.Context, id []byte, key store.AppStateSyncKey) error {
	s.mu.Lock()
	k := key
	s.appStateKeys[b64(id)] = &k
	s.mu.Unlock()
	s.changed()
	return nil
}

func (s *Store) GetAppStateSyncKey(ctx context.Context, id []byte) (*store.AppStateSyncKey, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k, ok := s.appStateKeys[b64(id)]
	if !ok {
		return nil, nil
	}
	c := *k
	return &c, nil
}

func (s *Store) GetLatestAppStateSyncKeyID(ctx context.Context) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var bestID string
	var bestTS int64 = -1
	for id, k := range s.appStateKeys {
		if k.Timestamp > bestTS {
			bestTS = k.Timestamp
			bestID = id
		}
	}
	if bestTS < 0 {
		return nil, nil
	}
	return base64.StdEncoding.DecodeString(bestID)
}

func (s *Store) GetAllAppStateSyncKeys(ctx context.Context) ([]*store.AppStateSyncKey, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*store.AppStateSyncKey, 0, len(s.appStateKeys))
	for _, k := range s.appStateKeys {
		c := *k
		out = append(out, &c)
	}
	return out, nil
}

// ---- AppStateStore ----

func (s *Store) PutAppStateVersion(ctx context.Context, name string, version uint64, hash [128]byte) error {
	s.mu.Lock()
	s.appStateVers[name] = &appStateVersion{Version: version, Hash: hash[:]}
	s.mu.Unlock()
	s.changed()
	return nil
}

func (s *Store) GetAppStateVersion(ctx context.Context, name string) (uint64, [128]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var hash [128]byte
	v, ok := s.appStateVers[name]
	if !ok {
		return 0, hash, nil
	}
	if len(v.Hash) != 128 {
		return 0, hash, errors.New("invalid app state hash length")
	}
	copy(hash[:], v.Hash)
	return v.Version, hash, nil
}

func (s *Store) DeleteAppStateVersion(ctx context.Context, name string) error {
	s.mu.Lock()
	delete(s.appStateVers, name)
	prefix := name + "|"
	for k := range s.appStateMACs {
		if strings.HasPrefix(k, prefix) {
			delete(s.appStateMACs, k)
		}
	}
	s.mu.Unlock()
	s.changed()
	return nil
}

func (s *Store) PutAppStateMutationMACs(ctx context.Context, name string, version uint64, mutations []store.AppStateMutationMAC) error {
	s.mu.Lock()
	for _, m := range mutations {
		s.appStateMACs[name+"|"+b64(m.IndexMAC)] = m.ValueMAC
	}
	s.mu.Unlock()
	s.changed()
	return nil
}

func (s *Store) DeleteAppStateMutationMACs(ctx context.Context, name string, indexMACs [][]byte) error {
	s.mu.Lock()
	for _, m := range indexMACs {
		delete(s.appStateMACs, name+"|"+b64(m))
	}
	s.mu.Unlock()
	s.changed()
	return nil
}

func (s *Store) GetAppStateMutationMAC(ctx context.Context, name string, indexMAC []byte) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.appStateMACs[name+"|"+b64(indexMAC)], nil
}

// ---- ContactStore ----

func (s *Store) getContactLocked(user types.JID) *types.ContactInfo {
	key := user.ToNonAD().String()
	c, ok := s.contacts[key]
	if !ok {
		c = &types.ContactInfo{}
		s.contacts[key] = c
	}
	return c
}

func (s *Store) PutPushName(ctx context.Context, user types.JID, pushName string) (bool, string, error) {
	s.mu.Lock()
	c := s.getContactLocked(user)
	if c.PushName == pushName {
		s.mu.Unlock()
		return false, "", nil
	}
	prev := c.PushName
	c.PushName = pushName
	c.Found = true
	s.mu.Unlock()
	s.changed()
	return true, prev, nil
}

func (s *Store) PutBusinessName(ctx context.Context, user types.JID, businessName string) (bool, string, error) {
	s.mu.Lock()
	c := s.getContactLocked(user)
	if c.BusinessName == businessName {
		s.mu.Unlock()
		return false, "", nil
	}
	prev := c.BusinessName
	c.BusinessName = businessName
	c.Found = true
	s.mu.Unlock()
	s.changed()
	return true, prev, nil
}

func (s *Store) PutContactName(ctx context.Context, user types.JID, fullName, firstName string) error {
	s.mu.Lock()
	c := s.getContactLocked(user)
	c.FullName = fullName
	c.FirstName = firstName
	c.Found = true
	s.mu.Unlock()
	s.changed()
	return nil
}

func (s *Store) PutAllContactNames(ctx context.Context, contacts []store.ContactEntry) error {
	s.mu.Lock()
	for _, e := range contacts {
		c := s.getContactLocked(e.JID)
		c.FullName = e.FullName
		c.FirstName = e.FirstName
		c.Found = true
	}
	s.mu.Unlock()
	s.changed()
	return nil
}

func (s *Store) PutManyRedactedPhones(ctx context.Context, entries []store.RedactedPhoneEntry) error {
	s.mu.Lock()
	for _, e := range entries {
		c := s.getContactLocked(e.JID)
		c.RedactedPhone = e.RedactedPhone
		c.Found = true
	}
	s.mu.Unlock()
	s.changed()
	return nil
}

func (s *Store) GetContact(ctx context.Context, user types.JID) (types.ContactInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.contacts[user.ToNonAD().String()]
	if !ok {
		return types.ContactInfo{}, nil
	}
	return *c, nil
}

func (s *Store) GetAllContacts(ctx context.Context) (map[types.JID]types.ContactInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[types.JID]types.ContactInfo, len(s.contacts))
	for k, c := range s.contacts {
		jid, err := types.ParseJID(k)
		if err != nil {
			continue
		}
		out[jid] = *c
	}
	return out, nil
}

// ---- ChatSettingsStore ----

func (s *Store) chatLocked(chat types.JID) *chatSettings {
	key := chat.String()
	c, ok := s.chatSettings[key]
	if !ok {
		c = &chatSettings{}
		s.chatSettings[key] = c
	}
	return c
}

func (s *Store) PutMutedUntil(ctx context.Context, chat types.JID, mutedUntil time.Time) error {
	s.mu.Lock()
	c := s.chatLocked(chat)
	if mutedUntil.IsZero() {
		c.MutedUntil = 0
	} else if mutedUntil.Equal(store.MutedForever) {
		c.MutedUntil = -1
	} else {
		c.MutedUntil = mutedUntil.Unix()
	}
	s.mu.Unlock()
	s.changed()
	return nil
}

func (s *Store) PutPinned(ctx context.Context, chat types.JID, pinned bool) error {
	s.mu.Lock()
	s.chatLocked(chat).Pinned = pinned
	s.mu.Unlock()
	s.changed()
	return nil
}

func (s *Store) PutArchived(ctx context.Context, chat types.JID, archived bool) error {
	s.mu.Lock()
	s.chatLocked(chat).Archived = archived
	s.mu.Unlock()
	s.changed()
	return nil
}

func (s *Store) PutWASARootSecretID(ctx context.Context, chat types.JID, id types.MessageID) error {
	s.mu.Lock()
	s.chatLocked(chat).WASARoot = id
	s.mu.Unlock()
	s.changed()
	return nil
}

func (s *Store) GetWASARootSecretID(ctx context.Context, chat types.JID) (types.MessageID, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c, ok := s.chatSettings[chat.String()]; ok {
		return c.WASARoot, nil
	}
	return "", nil
}

func (s *Store) GetChatSettings(ctx context.Context, chat types.JID) (types.LocalChatSettings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out types.LocalChatSettings
	c, ok := s.chatSettings[chat.String()]
	if !ok {
		return out, nil
	}
	out.Found = true
	out.Pinned = c.Pinned
	out.Archived = c.Archived
	if c.MutedUntil < 0 {
		out.MutedUntil = store.MutedForever
	} else if c.MutedUntil > 0 {
		out.MutedUntil = time.Unix(c.MutedUntil, 0)
	}
	return out, nil
}

// ---- MsgSecretStore ----

func secretKey(chat, sender types.JID, id types.MessageID) string {
	return chat.ToNonAD().String() + "|" + sender.ToNonAD().String() + "|" + id
}

func (s *Store) PutMessageSecrets(ctx context.Context, inserts []store.MessageSecretInsert) error {
	s.mu.Lock()
	for _, in := range inserts {
		s.msgSecrets[secretKey(in.Chat, in.Sender, in.ID)] = &msgSecret{Secret: in.Secret, Sender: in.Sender.ToNonAD().String()}
	}
	s.mu.Unlock()
	s.changed()
	return nil
}

func (s *Store) PutMessageSecret(ctx context.Context, chat, sender types.JID, id types.MessageID, secret []byte) error {
	return s.PutMessageSecrets(ctx, []store.MessageSecretInsert{{Chat: chat, Sender: sender, ID: id, Secret: secret}})
}

func (s *Store) GetMessageSecret(ctx context.Context, chat, sender types.JID, id types.MessageID) ([]byte, types.JID, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.msgSecrets[secretKey(chat, sender, id)]
	if !ok {
		return nil, types.EmptyJID, nil
	}
	j, _ := types.ParseJID(m.Sender)
	return m.Secret, j, nil
}

// ---- PrivacyTokenStore ----

func (s *Store) PutPrivacyTokens(ctx context.Context, tokens ...store.PrivacyToken) error {
	s.mu.Lock()
	for _, t := range tokens {
		tt := t
		tt.User = t.User.ToNonAD()
		s.privTokens[tt.User.String()] = &tt
	}
	s.mu.Unlock()
	s.changed()
	return nil
}

func (s *Store) GetPrivacyToken(ctx context.Context, user types.JID) (*store.PrivacyToken, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.privTokens[user.ToNonAD().String()]
	if !ok {
		return nil, nil
	}
	c := *t
	return &c, nil
}

func (s *Store) DeleteExpiredPrivacyTokens(ctx context.Context, cutoff time.Time) (int64, error) {
	s.mu.Lock()
	var n int64
	for k, t := range s.privTokens {
		if t.Timestamp.Before(cutoff) {
			delete(s.privTokens, k)
			n++
		}
	}
	s.mu.Unlock()
	if n > 0 {
		s.changed()
	}
	return n, nil
}

// ---- NCTSaltStore ----

func (s *Store) PutNCTSalt(ctx context.Context, salt []byte) error {
	s.mu.Lock()
	s.nctSalt = salt
	s.mu.Unlock()
	s.changed()
	return nil
}

func (s *Store) GetNCTSalt(ctx context.Context) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.nctSalt, nil
}

func (s *Store) DeleteNCTSalt(ctx context.Context) error {
	s.mu.Lock()
	s.nctSalt = nil
	s.mu.Unlock()
	s.changed()
	return nil
}

// ---- EventBuffer ----

func (s *Store) GetBufferedEvent(ctx context.Context, ciphertextHash [32]byte) (*store.BufferedEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.bufEvents[b64(ciphertextHash[:])]
	if !ok {
		return nil, nil
	}
	c := *e
	return &c, nil
}

func (s *Store) PutBufferedEvent(ctx context.Context, ciphertextHash [32]byte, plaintext []byte, serverTimestamp time.Time) error {
	s.mu.Lock()
	s.bufEvents[b64(ciphertextHash[:])] = &store.BufferedEvent{Plaintext: plaintext, InsertTime: time.Now(), ServerTime: serverTimestamp}
	s.mu.Unlock()
	return nil
}

func (s *Store) DoDecryptionTxn(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}

func (s *Store) ClearBufferedEventPlaintext(ctx context.Context, ciphertextHash [32]byte) error {
	s.mu.Lock()
	if e, ok := s.bufEvents[b64(ciphertextHash[:])]; ok {
		e.Plaintext = nil
	}
	s.mu.Unlock()
	return nil
}

func (s *Store) DeleteOldBufferedHashes(ctx context.Context) error {
	s.mu.Lock()
	cutoff := time.Now().Add(-14 * 24 * time.Hour)
	for k, e := range s.bufEvents {
		if e.InsertTime.Before(cutoff) {
			delete(s.bufEvents, k)
		}
	}
	s.mu.Unlock()
	return nil
}

func (s *Store) GetOutgoingEvent(ctx context.Context, chatJID, altChatJID types.JID, id types.MessageID) (string, []byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, ok := s.outEvents[chatJID.String()+"|"+id]; ok {
		return e.Format, e.Plaintext, nil
	}
	if e, ok := s.outEvents[altChatJID.String()+"|"+id]; ok {
		return e.Format, e.Plaintext, nil
	}
	return "", nil, fmt.Errorf("outgoing event %s not found", id)
}

func (s *Store) AddOutgoingEvent(ctx context.Context, chatJID types.JID, id types.MessageID, format string, plaintext []byte) error {
	s.mu.Lock()
	s.outEvents[chatJID.String()+"|"+id] = &outgoingEvent{Format: format, Plaintext: plaintext, Time: time.Now()}
	s.mu.Unlock()
	s.changed()
	return nil
}

func (s *Store) DeleteOldOutgoingEvents(ctx context.Context) error {
	s.mu.Lock()
	cutoff := time.Now().Add(-14 * 24 * time.Hour)
	n := 0
	for k, e := range s.outEvents {
		if e.Time.Before(cutoff) {
			delete(s.outEvents, k)
			n++
		}
	}
	s.mu.Unlock()
	if n > 0 {
		s.changed()
	}
	return nil
}

// ---- LIDStore ----

func (s *Store) PutManyLIDMappings(ctx context.Context, mappings []store.LIDMapping) error {
	s.mu.Lock()
	for _, m := range mappings {
		s.lidToPN[m.LID.User] = m.PN.User
		s.pnToLID[m.PN.User] = m.LID.User
	}
	s.mu.Unlock()
	s.changed()
	return nil
}

func (s *Store) PutLIDMapping(ctx context.Context, lid, jid types.JID) error {
	return s.PutManyLIDMappings(ctx, []store.LIDMapping{{LID: lid, PN: jid}})
}

func (s *Store) GetPNForLID(ctx context.Context, lid types.JID) (types.JID, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	pn, ok := s.lidToPN[lid.User]
	if !ok {
		return types.EmptyJID, nil
	}
	return types.JID{User: pn, Server: types.DefaultUserServer, Device: lid.Device}, nil
}

func (s *Store) GetLIDForPN(ctx context.Context, pn types.JID) (types.JID, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	lid, ok := s.pnToLID[pn.User]
	if !ok {
		return types.EmptyJID, nil
	}
	return types.JID{User: lid, Server: types.HiddenUserServer, Device: pn.Device}, nil
}

func (s *Store) GetManyLIDsForPNs(ctx context.Context, pns []types.JID) (map[types.JID]types.JID, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[types.JID]types.JID, len(pns))
	for _, pn := range pns {
		if lid, ok := s.pnToLID[pn.User]; ok {
			out[pn] = types.JID{User: lid, Server: types.HiddenUserServer, Device: pn.Device}
		}
	}
	return out, nil
}

var _ store.AllStores = (*Store)(nil)
var _ store.DeviceContainer = (*Store)(nil)
