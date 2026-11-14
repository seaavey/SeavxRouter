package qoder

import (
	"bufio"
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"seavxrouter/internal/auth"
	"seavxrouter/internal/backend"
)

// --- Qoder regions ---

type qoderRegion struct {
	Name     string
	AuthBase string
	ChatBase string
}

var (
	qoderCN = &qoderRegion{
		Name:     "cn",
		AuthBase: "https://gateway.qoder.com.cn",
		ChatBase: "https://gateway.qoder.com.cn",
	}
	qoderGlobal = &qoderRegion{
		Name:     "global",
		AuthBase: "https://api1.qoder.sh",
		ChatBase: "https://api1.qoder.sh",
	}
)

// qoderRegionFor maps a region name to its configuration. Global (qoder.sh)
// is the default; CN is only used when explicitly requested.
func qoderRegionFor(name string) *qoderRegion {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "cn", "china", "mainland":
		return qoderCN
	default:
		return qoderGlobal
	}
}

// --- COSY wire encoding (custom base64 with a rotated alphabet) ---

const (
	qoderCustomAlphabet = "_doRTgHZBKcGVjlvpC,@aFSx#DPuNJme&i*MzLOEn)sUrthbf%Y^w.(kIQyXqWA!"
	qoderStdAlphabet    = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	qoderAppCode        = "cosy"
	qoderClientVersion  = "0.1.43"
	qoderClientType     = "5"
	qoderDefaultSecret  = "d2FyLCB3YXIgbmV2ZXIgY2hhbmdlcw=="
	qoderUserAgent      = "Go-http-client/2.0"
	qoderBusyCode       = "10605"
	qoderDefaultTier    = "personal_standard"
	qoderDefaultMaxOut  = 32000
	qoderMaxToolCalls   = 128
)

var (
	qoderS2C = func() [128]byte {
		var m [128]byte
		for i := range m {
			m[i] = 0xff
		}
		for i := 0; i < len(qoderStdAlphabet); i++ {
			m[qoderStdAlphabet[i]] = qoderCustomAlphabet[i]
		}
		// Base64 padding has its own custom character: "=" <-> "$".
		m['='] = '$'
		return m
	}()
	qoderC2S = func() [128]byte {
		var m [128]byte
		for i := range m {
			m[i] = 0xff
		}
		for i := 0; i < len(qoderCustomAlphabet); i++ {
			m[qoderCustomAlphabet[i]] = qoderStdAlphabet[i]
		}
		m['$'] = '='
		return m
	}()
)

// qoderEncode transforms JSON bytes into the gateway's custom base64 variant:
// standard base64 with the outer thirds rotated, then every character mapped
// through the custom alphabet.
func qoderEncode(plain []byte) string {
	std := base64.StdEncoding.EncodeToString(plain)
	n := len(std)
	if n == 0 {
		return ""
	}
	a := n / 3
	rearranged := make([]byte, 0, n)
	rearranged = append(rearranged, std[n-a:]...)
	rearranged = append(rearranged, std[a:n-a]...)
	rearranged = append(rearranged, std[:a]...)
	out := make([]byte, n)
	for i, ch := range rearranged {
		if ch >= 128 || qoderS2C[ch] == 0xff {
			out[i] = ch
			continue
		}
		out[i] = qoderS2C[ch]
	}
	return string(out)
}

func qoderDecode(encoded string) ([]byte, error) {
	n := len(encoded)
	mapped := make([]byte, n)
	for i := 0; i < n; i++ {
		ch := encoded[i]
		if ch >= 128 || qoderC2S[ch] == 0xff {
			return nil, fmt.Errorf("qoder decode: %q not in alphabet", ch)
		}
		mapped[i] = qoderC2S[ch]
	}
	a := n / 3
	std := make([]byte, n)
	copy(std[:a], mapped[n-a:])
	copy(std[a:n-a], mapped[a:n-a])
	copy(std[n-a:], mapped[:a])
	return base64.StdEncoding.DecodeString(string(std))
}

// --- Machine signature ---

func qoderSecret() string {
	if s := os.Getenv("QODER_SIGNATURE_SECRET"); s != "" {
		return s
	}
	return qoderDefaultSecret
}

func qoderMD5(s string) string {
	h := md5.Sum([]byte(s))
	return hex.EncodeToString(h[:])
}

func qoderCurrentDate() string {
	return time.Now().UTC().Format("Mon, 02 Jan 2006 15:04:05 GMT")
}

func qoderSign(date string) string {
	return qoderMD5(qoderAppCode + "&" + qoderSecret() + "&" + date)
}

// qoderMachineIdentity derives stable machine identifiers from the PAT so a
// restart reuses the same identity instead of registering a new device.
func qoderMachineIdentity(pat string) (machineID, machineToken, machineType string) {
	sum := sha256.Sum256([]byte("qoder-machine:" + pat))
	h := hex.EncodeToString(sum[:])
	machineID = h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
	machineToken = h[32:64]
	machineType = machineID[:18]
	return machineID, machineToken, machineType
}

// --- Session envelope (RSA key wrap + AES payload) ---

const qoderServerPubKeyPEM = `-----BEGIN PUBLIC KEY-----
MIGfMA0GCSqGSIb3DQEBAQUAA4GNADCBiQKBgQDA8iMH5c02LilrsERw9t6Pv5Nc
4k6Pz1EaDicBMpdpxKduSZu5OANqUq8er4GM95omAGIOPOh+Nx0spthYA2BqGz+l
6HRkPJ7S236FZz73In/KVuLnwI8JJ2CbuJap8kvheCCZpmAWpb/cPx/3Vr/J6I17
XcW+ML9FoCI6AOvOzwIDAQAB
-----END PUBLIC KEY-----`

var qoderPubKey = func() *rsa.PublicKey {
	block, _ := pem.Decode([]byte(qoderServerPubKeyPEM))
	if block == nil {
		panic("qoder: bad server public key PEM")
	}
	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		panic("qoder: bad server public key: " + err.Error())
	}
	return pub.(*rsa.PublicKey)
}()

type qoderIdentity struct {
	Name          string
	Aid           string
	UID           string
	UserType      string
	SecurityOauth string
	RefreshToken  string
}

type qoderIdentityPayload struct {
	Name               string `json:"name"`
	Aid                string `json:"aid"`
	UID                string `json:"uid"`
	YxUID              string `json:"yx_uid"`
	OrganizationID     string `json:"organization_id"`
	OrganizationName   string `json:"organization_name"`
	UserType           string `json:"user_type"`
	SecurityOauthToken string `json:"security_oauth_token"`
	RefreshToken       string `json:"refresh_token"`
}

type qoderSession struct {
	CosyKey      string
	Info         string
	UID          string
	UserType     string
	MachineID    string
	MachineToken string
	MachineType  string
}

type qoderPayload struct {
	CosyVersion string `json:"cosyVersion"`
	IdeVersion  string `json:"ideVersion"`
	Info        string `json:"info"`
	RequestID   string `json:"requestId"`
	Version     string `json:"version"`
}

// qoderMarshalNoEscape matches the client's compact, non-HTML-escaped JSON.
func qoderMarshalNoEscape(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

func qoderAESEncrypt(plain, key []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	pad := aes.BlockSize - len(plain)%aes.BlockSize
	padded := make([]byte, len(plain)+pad)
	copy(padded, plain)
	for i := len(plain); i < len(padded); i++ {
		padded[i] = byte(pad)
	}
	out := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, key[:aes.BlockSize]).CryptBlocks(out, padded)
	return out, nil
}

func qoderNewSession(identity qoderIdentity, machineID, machineToken, machineType string) (*qoderSession, error) {
	tempKey := []byte(backend.UUID()[:16])
	enc, err := rsa.EncryptPKCS1v15(rand.Reader, qoderPubKey, tempKey)
	if err != nil {
		return nil, fmt.Errorf("qoder session: %w", err)
	}
	payload, err := qoderMarshalNoEscape(qoderIdentityPayload{
		Name:               identity.Name,
		Aid:                identity.Aid,
		UID:                identity.UID,
		UserType:           identity.UserType,
		SecurityOauthToken: identity.SecurityOauth,
		RefreshToken:       identity.RefreshToken,
	})
	if err != nil {
		return nil, err
	}
	info, err := qoderAESEncrypt(payload, tempKey)
	if err != nil {
		return nil, fmt.Errorf("qoder session: %w", err)
	}
	return &qoderSession{
		CosyKey:      base64.StdEncoding.EncodeToString(enc),
		Info:         base64.StdEncoding.EncodeToString(info),
		UID:          identity.UID,
		UserType:     identity.UserType,
		MachineID:    machineID,
		MachineToken: machineToken,
		MachineType:  machineType,
	}, nil
}

func (s *qoderSession) bearer(date, body, pathSig string) string {
	payload, err := qoderMarshalNoEscape(qoderPayload{
		CosyVersion: qoderClientVersion,
		IdeVersion:  "",
		Info:        s.Info,
		RequestID:   backend.UUID(),
		Version:     "v1",
	})
	if err != nil {
		return ""
	}
	payloadB64 := base64.StdEncoding.EncodeToString(payload)
	sig := qoderMD5(payloadB64 + "\n" + s.CosyKey + "\n" + date + "\n" + body + "\n" + pathSig)
	return "Bearer COSY." + payloadB64 + "." + sig
}

func qoderSigPath(fullURL string) string {
	u, err := url.Parse(fullURL)
	if err != nil {
		return fullURL
	}
	path := u.Path
	if strings.HasPrefix(path, "/algo") {
		path = path[len("/algo"):]
	}
	return path
}

func qoderMachineHeaders(machineID, machineToken, machineType, date, sig string) http.Header {
	h := http.Header{}
	h.Set("cosy-machinetoken", machineToken)
	h.Set("cosy-machinetype", machineType)
	h.Set("login-version", "v2")
	h.Set("appcode", qoderAppCode)
	h.Set("accept", "application/json")
	h.Set("accept-encoding", "identity")
	h.Set("cosy-version", qoderClientVersion)
	h.Set("cosy-clienttype", qoderClientType)
	h.Set("date", date)
	h.Set("signature", sig)
	h.Set("content-type", "application/json")
	h.Set("cosy-machineid", machineID)
	h.Set("user-agent", qoderUserAgent)
	return h
}

func qoderSessionHeaders(sess *qoderSession, date, bearer, accept string) http.Header {
	h := http.Header{}
	h.Set("cosy-data-policy", "AGREE")
	h.Set("content-type", "application/json")
	h.Set("cosy-machinetype", sess.MachineType)
	h.Set("cosy-clienttype", qoderClientType)
	h.Set("cosy-date", date)
	h.Set("cosy-user", sess.UID)
	h.Set("cosy-key", sess.CosyKey)
	h.Set("accept", accept)
	h.Set("authorization", bearer)
	h.Set("accept-encoding", "identity")
	h.Set("cosy-version", qoderClientVersion)
	h.Set("cosy-machineid", sess.MachineID)
	h.Set("cosy-machinetoken", sess.MachineToken)
	h.Set("login-version", "v2")
	h.Set("user-agent", qoderUserAgent)
	return h
}

// --- Engine ---

type qoderAccount struct {
	auth         *auth.Auth
	region       *qoderRegion
	machineID    string
	machineToken string
	machineType  string

	mu            sync.Mutex
	sess          *qoderSession
	userType      string
	securityOauth string
	refreshToken  string
	expireAt      time.Time
	refreshedAt   time.Time
	bootstrapped  bool

	refreshMu sync.Mutex
}

type QoderEngine struct {
	Store        auth.AuthStore
	HTTP         *http.Client
	DefaultModel string
	AuthBase     string // test override for both regions
	ChatBase     string // test override for both regions
	Region       string // preferred login region ("cn"/"global")
	Stdin        io.Reader
	Out          io.Writer

	mu    sync.Mutex
	auths []*auth.Auth
	cache auth.AccountCache
	next  int
	acct  map[*auth.Auth]*qoderAccount

	modelsMu  sync.Mutex
	catalog   *qoderCatalog
	catalogAt time.Time

	quotaMu  sync.Mutex
	quotaErr map[*auth.Auth]time.Time
}

const (
	qoderRenewMargin  = 2 * time.Hour
	qoderUnknownTTL   = 30 * time.Minute
	qoderCatalogTTL   = 10 * time.Minute
	qoderBusyFallback = 2 * time.Second
)

var qoderBusyBackoff = qoderBusyFallback

func NewQoderEngine(store auth.AuthStore, auths []*auth.Auth) (*QoderEngine, error) {
	if len(auths) == 0 {
		return nil, fmt.Errorf("no qoder accounts")
	}
	return &QoderEngine{Store: store, HTTP: &http.Client{}, auths: auths, cache: auth.NewAccountCache(store, "qoder")}, nil
}

func (e *QoderEngine) client() *http.Client {
	if e.HTTP != nil {
		return e.HTTP
	}
	return http.DefaultClient
}

func (e *QoderEngine) authBase(r *qoderRegion) string {
	if e.AuthBase != "" {
		return strings.TrimRight(e.AuthBase, "/")
	}
	return r.AuthBase
}

func (e *QoderEngine) chatBase(r *qoderRegion) string {
	if e.ChatBase != "" {
		return strings.TrimRight(e.ChatBase, "/")
	}
	return r.ChatBase
}

func (e *QoderEngine) accountFor(a *auth.Auth) *qoderAccount {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.acct == nil {
		e.acct = map[*auth.Auth]*qoderAccount{}
	}
	if acct := e.acct[a]; acct != nil {
		return acct
	}
	acct := &qoderAccount{auth: a, region: qoderRegionFor(a.Region), userType: a.Tier}
	acct.machineID, acct.machineToken, acct.machineType = qoderMachineIdentity(a.AccessToken)
	e.acct[a] = acct
	return acct
}

func (e *QoderEngine) snapshotAuths() ([]*auth.Auth, int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.auths = e.cache.Refresh(e.auths)
	return append([]*auth.Auth(nil), e.auths...), e.next
}

func (e *QoderEngine) trackQuota(a *auth.Auth, err error) {
	e.quotaMu.Lock()
	defer e.quotaMu.Unlock()
	if err == nil {
		delete(e.quotaErr, a)
		return
	}
	if !backend.IsQuotaError(err) {
		return
	}
	if e.quotaErr == nil {
		e.quotaErr = map[*auth.Auth]time.Time{}
	}
	e.quotaErr[a] = time.Now()
}

func (e *QoderEngine) Default() string {
	if e.DefaultModel != "" {
		return e.DefaultModel
	}
	e.modelsMu.Lock()
	cat := e.catalog
	e.modelsMu.Unlock()
	if cat != nil {
		return cat.defaultName
	}
	return qoderFallbackCatalog().defaultName
}

// --- HTTP error classification ---

type qoderAuthError struct {
	status int
	detail string
}

func (e *qoderAuthError) Error() string {
	return fmt.Sprintf("qoder HTTP %d: %s", e.status, e.detail)
}

type qoderBusyError struct {
	status     int
	message    string
	retryAfter time.Duration
}

func (e *qoderBusyError) Error() string {
	s := "qoder gateway busy (code " + qoderBusyCode + ")"
	if e.message != "" {
		s += ": " + e.message
	}
	if e.retryAfter > 0 {
		s += fmt.Sprintf(" (retry after %s)", e.retryAfter)
	}
	return s
}

func qoderTruncateBody(body []byte) string {
	s := strings.Join(strings.Fields(string(body)), " ")
	r := []rune(s)
	if len(r) > 400 {
		return string(r[:400]) + "..."
	}
	return s
}

func qoderCodeString(v any) string {
	switch c := v.(type) {
	case string:
		return c
	case float64:
		if c == float64(int64(c)) {
			return strconv.FormatInt(int64(c), 10)
		}
	case json.Number:
		return c.String()
	}
	return ""
}

func qoderBusyRetryAfter(msg string) time.Duration {
	var meta struct {
		RetryAfterSeconds int `json:"retryAfterSeconds"`
	}
	if strings.HasPrefix(strings.TrimSpace(msg), "{") && json.Unmarshal([]byte(msg), &meta) == nil && meta.RetryAfterSeconds > 0 {
		return time.Duration(meta.RetryAfterSeconds) * time.Second
	}
	return 0
}

func qoderBusyFromEnvelope(body []byte) *qoderBusyError {
	s := strings.TrimSpace(string(body))
	if i := strings.IndexByte(s, '{'); i > 0 {
		s = s[i:]
	}
	var outer struct {
		Code    json.RawMessage `json:"code"`
		Message string          `json:"message"`
	}
	if json.Unmarshal([]byte(s), &outer) != nil || qoderCodeString(outer.Code) != qoderBusyCode {
		return nil
	}
	busy := &qoderBusyError{}
	if outer.Message != "" {
		busy.message = outer.Message
		busy.retryAfter = qoderBusyRetryAfter(outer.Message)
	}
	return busy
}

func qoderAPIError(status int, body []byte) error {
	if status == 401 || status == 403 {
		if busy := qoderBusyFromEnvelope(body); busy != nil {
			busy.status = status
			return busy
		}
		return &qoderAuthError{status: status, detail: qoderTruncateBody(body)}
	}
	return fmt.Errorf("qoder HTTP %d: %s", status, qoderTruncateBody(body))
}

func qoderRotatable(err error) bool {
	var authErr *qoderAuthError
	return backend.IsQuotaError(err) || backend.IsAuthError(err) || errors.As(err, &authErr)
}

// --- auth.Auth endpoints (machine-signature authenticated) ---

type qoderJobTokenInner struct {
	PersonalToken      string          `json:"personalToken"`
	SecurityOauthToken string          `json:"securityOauthToken"`
	RefreshToken       string          `json:"refreshToken"`
	NeedRefresh        bool            `json:"needRefresh"`
	AuthInfo           json.RawMessage `json:"authInfo"`
}

type qoderOuterPayload struct {
	Payload       string `json:"payload"`
	EncodeVersion string `json:"encodeVersion"`
}

func (e *QoderEngine) requestJobToken(ctx context.Context, acct *qoderAccount, refreshToken, securityOauth string, needRefresh bool) (map[string]any, error) {
	inner, err := qoderMarshalNoEscape(qoderJobTokenInner{
		PersonalToken:      acct.auth.AccessToken,
		SecurityOauthToken: securityOauth,
		RefreshToken:       refreshToken,
		NeedRefresh:        needRefresh,
		AuthInfo:           json.RawMessage("{}"),
	})
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	urlStr := e.authBase(acct.region) + "/algo/api/v3/user/jobToken?Encode=1"
	return e.postEncoded(ctx, acct, urlStr, qoderOuterPayload{Payload: string(inner), EncodeVersion: "1"})
}

func (e *QoderEngine) postEncoded(ctx context.Context, acct *qoderAccount, urlStr string, obj any) (map[string]any, error) {
	date := qoderCurrentDate()
	plain, err := qoderMarshalNoEscape(obj)
	if err != nil {
		return nil, err
	}
	body := qoderEncode(plain)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, urlStr, strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header = qoderMachineHeaders(acct.machineID, acct.machineToken, acct.machineType, date, qoderSign(date))
	return e.doJSON(req)
}

func (e *QoderEngine) doJSON(req *http.Request) (map[string]any, error) {
	resp, err := e.client().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
		return nil, qoderAPIError(resp.StatusCode, detail)
	}
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

func (e *QoderEngine) fetchUserType(ctx context.Context, acct *qoderAccount, userID string) string {
	if userID == "" {
		return ""
	}
	inner, err := qoderMarshalNoEscape(struct {
		UserID             string          `json:"userId"`
		PersonalToken      string          `json:"personalToken"`
		SecurityOauthToken string          `json:"securityOauthToken"`
		RefreshToken       string          `json:"refreshToken"`
		NeedRefresh        bool            `json:"needRefresh"`
		AuthInfo           json.RawMessage `json:"authInfo"`
	}{UserID: userID, AuthInfo: json.RawMessage("{}")})
	if err != nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	urlStr := e.authBase(acct.region) + "/algo/api/v3/user/status?Encode=1"
	raw, err := e.postEncoded(ctx, acct, urlStr, qoderOuterPayload{Payload: string(inner), EncodeVersion: "1"})
	if err != nil {
		slog.Warn("qoder user/status failed", "err", err)
		return ""
	}
	if t, _ := raw["userType"].(string); t != "" {
		return t
	}
	return ""
}

// --- Session lifecycle ---

func (e *QoderEngine) ensureSession(ctx context.Context, acct *qoderAccount) (*qoderSession, error) {
	acct.mu.Lock()
	sess := acct.sess
	fresh := qoderSessionFresh(acct)
	acct.mu.Unlock()
	if sess != nil && fresh {
		return sess, nil
	}
	return e.renewSession(ctx, acct, false)
}

func qoderSessionFresh(acct *qoderAccount) bool {
	if acct.sess == nil {
		return false
	}
	if acct.expireAt.IsZero() {
		return time.Since(acct.refreshedAt) < qoderUnknownTTL
	}
	return time.Now().Before(acct.expireAt.Add(-qoderRenewMargin))
}

func (e *QoderEngine) renewSession(ctx context.Context, acct *qoderAccount, force bool) (*qoderSession, error) {
	acct.refreshMu.Lock()
	defer acct.refreshMu.Unlock()

	acct.mu.Lock()
	if !force && acct.bootstrapped && qoderSessionFresh(acct) {
		sess := acct.sess
		acct.mu.Unlock()
		return sess, nil
	}
	bootstrapped := acct.bootstrapped
	refreshToken := acct.refreshToken
	securityOauth := acct.securityOauth
	acct.mu.Unlock()

	var jt map[string]any
	var err error
	if bootstrapped && refreshToken != "" {
		jt, err = e.requestJobToken(ctx, acct, refreshToken, securityOauth, true)
		if err != nil && force {
			slog.Warn("qoder refresh token rejected; re-exchanging PAT", "err", err)
			jt, err = e.requestJobToken(ctx, acct, "", "", false)
		}
	} else {
		jt, err = e.requestJobToken(ctx, acct, "", "", false)
	}
	if err != nil {
		return nil, err
	}
	return e.applyJobToken(ctx, acct, jt)
}

func (e *QoderEngine) applyJobToken(ctx context.Context, acct *qoderAccount, jt map[string]any) (*qoderSession, error) {
	name := qoderString(jt["name"])
	id := qoderString(jt["id"])
	userType := e.fetchUserType(ctx, acct, id)
	if userType == "" {
		acct.mu.Lock()
		userType = acct.userType
		acct.mu.Unlock()
	}
	if userType == "" {
		userType = qoderString(jt["userType"])
	}
	if userType == "" {
		userType = qoderDefaultTier
	}
	securityOauth := qoderString(jt["securityOauthToken"])
	refreshToken := qoderString(jt["refreshToken"])
	sess, err := qoderNewSession(qoderIdentity{
		Name:          name,
		Aid:           id,
		UID:           id,
		UserType:      userType,
		SecurityOauth: securityOauth,
		RefreshToken:  refreshToken,
	}, acct.machineID, acct.machineToken, acct.machineType)
	if err != nil {
		return nil, err
	}
	expireMs := qoderInt64(jt["expireTime"])
	acct.mu.Lock()
	acct.sess = sess
	acct.userType = userType
	acct.securityOauth = securityOauth
	acct.refreshToken = refreshToken
	acct.bootstrapped = true
	acct.refreshedAt = time.Now()
	if expireMs > 0 {
		acct.expireAt = time.UnixMilli(expireMs)
	} else {
		acct.expireAt = time.Time{}
	}
	acct.mu.Unlock()
	slog.Info("qoder session ready", "account", name, "tier", userType, "region", acct.region.Name)
	return sess, nil
}

func qoderString(v any) string {
	s, _ := v.(string)
	return s
}

func qoderInt(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case json.Number:
		i, _ := n.Int64()
		return int(i)
	}
	return 0
}

func qoderInt64(v any) int64 {
	switch n := v.(type) {
	case float64:
		return int64(n)
	case int64:
		return n
	case int:
		return int64(n)
	case json.Number:
		i, _ := n.Int64()
		return i
	case string:
		i, _ := strconv.ParseInt(n, 10, 64)
		return i
	}
	return 0
}

// --- Model catalog ---

var qoderFallbackModels = map[string]string{
	"Qwen3.8-Max":       "qmodel_38max",
	"Qwen3.8-Flash":     "qfmodel",
	"Qwen3.7-Max":       "qmodel_latest",
	"Qwen3.7-Plus":      "qmodel",
	"Qwen3.7-Flash":     "q37fmodel",
	"DeepSeek-V4-Pro":   "dmodel",
	"DeepSeek-Flash":    "dfmodel",
	"GLM-5.3":           "gmodel",
	"GLM-5.3-Flash":     "gfmodel",
	"GLM-5.2":           "gm51model",
	"Kimi-K3":           "kmodel_latest",
	"Kimi-K2.8-Preview": "kmodel",
	"MiniMax-M2.7":      "mmodel",
}

type qoderModelCaps struct {
	maxOutput int
	reasoning bool
}

type qoderCatalog struct {
	keys        map[string]string
	caps        map[string]qoderModelCaps
	defaultName string
}

func qoderFallbackCatalog() *qoderCatalog {
	keys := make(map[string]string, len(qoderFallbackModels))
	caps := make(map[string]qoderModelCaps, len(qoderFallbackModels))
	for name, key := range qoderFallbackModels {
		keys[name] = key
		caps[key] = qoderModelCaps{maxOutput: qoderDefaultMaxOut, reasoning: true}
	}
	cat := &qoderCatalog{keys: keys, caps: caps}
	cat.defaultName = qoderNameForKey(keys, "qmodel_latest")
	return cat
}

func qoderNameForKey(keys map[string]string, key string) string {
	for name, k := range keys {
		if k == key {
			return name
		}
	}
	names := make([]string, 0, len(keys))
	for name := range keys {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, want := range []string{"flash", "lite", "plus"} {
		for _, name := range names {
			if strings.Contains(strings.ToLower(name), want) {
				return name
			}
		}
	}
	if len(names) > 0 {
		return names[0]
	}
	return ""
}

func qoderTruthy(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case string:
		s := strings.ToLower(strings.TrimSpace(x))
		return !(s == "false" || s == "0")
	case float64:
		return x != 0
	case nil:
		return true
	default:
		return true
	}
}

func qoderPositiveInt(v any, def int) int {
	switch x := v.(type) {
	case float64:
		if n := int(x); float64(n) == x && n > 0 {
			return n
		}
	case string:
		if n, err := strconv.Atoi(strings.TrimSpace(x)); err == nil && n > 0 {
			return n
		}
	}
	return def
}

func qoderReasoningCapability(m map[string]any) bool {
	if tc, ok := m["thinking_config"]; ok {
		if tcMap, isMap := tc.(map[string]any); isMap {
			enabled, present := tcMap["enabled"]
			return present && enabled != nil
		}
		if tc != nil {
			return true
		}
	}
	if v, present := m["is_reasoning"]; present {
		return qoderTruthy(v)
	}
	return true
}

func qoderExtractCatalog(raw map[string]any) *qoderCatalog {
	keys := map[string]string{}
	caps := map[string]qoderModelCaps{}
	for _, scene := range []string{"chat", "app"} {
		list, _ := raw[scene].([]any)
		for _, item := range list {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if v, present := m["enable"]; present && !qoderTruthy(v) {
				continue
			}
			key := qoderString(m["key"])
			name := qoderString(m["display_name"])
			if key == "" || name == "" || key == "auto" {
				continue
			}
			if _, seen := keys[name]; seen {
				continue
			}
			keys[name] = key
			caps[key] = qoderModelCaps{
				maxOutput: qoderPositiveInt(m["max_output_tokens"], qoderDefaultMaxOut),
				reasoning: qoderReasoningCapability(m),
			}
		}
	}
	if len(keys) == 0 {
		return nil
	}
	cat := &qoderCatalog{keys: keys, caps: caps}
	cat.defaultName = qoderNameForKey(keys, "qmodel_latest")
	return cat
}

func (c *qoderCatalog) maxOutput(key string) int {
	if caps, ok := c.caps[key]; ok && caps.maxOutput > 0 {
		return caps.maxOutput
	}
	return qoderDefaultMaxOut
}

func (c *qoderCatalog) reasoning(key string) bool {
	if caps, ok := c.caps[key]; ok {
		return caps.reasoning
	}
	return true
}

func (c *qoderCatalog) supported() string {
	names := make([]string, 0, len(c.keys))
	for name := range c.keys {
		names = append(names, name)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

func (e *QoderEngine) catalogFor(ctx context.Context) *qoderCatalog {
	e.modelsMu.Lock()
	if e.catalog != nil && time.Since(e.catalogAt) < qoderCatalogTTL {
		cat := e.catalog
		e.modelsMu.Unlock()
		return cat
	}
	e.modelsMu.Unlock()

	cat, _ := e.fetchCatalog(ctx)
	if cat == nil {
		cat = qoderFallbackCatalog()
	}
	e.modelsMu.Lock()
	e.catalog = cat
	e.catalogAt = time.Now()
	e.modelsMu.Unlock()
	return cat
}

func (e *QoderEngine) fetchCatalog(ctx context.Context) (*qoderCatalog, error) {
	auths, _ := e.snapshotAuths()
	var lastErr error
	for _, a := range auths {
		cat, err := e.fetchCatalogWith(ctx, a)
		if err == nil {
			return cat, nil
		}
		lastErr = err
		slog.Warn("qoder model list failed", "err", err)
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no qoder accounts")
	}
	return nil, lastErr
}

func (e *QoderEngine) fetchCatalogWith(ctx context.Context, a *auth.Auth) (*qoderCatalog, error) {
	acct := e.accountFor(a)
	sess, err := e.ensureSession(ctx, acct)
	if err != nil {
		return nil, err
	}
	urlStr := e.chatBase(acct.region) + "/algo/api/v2/model/list?Encode=1"
	date := strconv.FormatInt(time.Now().Unix(), 10)
	fetchCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(fetchCtx, http.MethodGet, urlStr, nil)
	if err != nil {
		return nil, err
	}
	req.Header = qoderSessionHeaders(sess, date, sess.bearer(date, "", qoderSigPath(urlStr)), "application/json")
	raw, err := e.doJSON(req)
	if err != nil {
		return nil, err
	}
	cat := qoderExtractCatalog(raw)
	if cat == nil {
		return nil, fmt.Errorf("qoder model list response shape unexpected")
	}
	return cat, nil
}

// Check verifies each account against upstream.
func (e *QoderEngine) Check(ctx context.Context) []backend.AccountCheck {
	auths, _ := e.snapshotAuths()
	out := make([]backend.AccountCheck, 0, len(auths))
	for _, a := range auths {
		start := time.Now()
		cat, err := e.fetchCatalogWith(ctx, a)
		var models []backend.Model
		if err == nil {
			models = cat.models()
		}
		out = append(out, backend.AccountCheck{
			Account:   a.Email,
			AccountID: a.AccountID,
			Models:    models,
			Err:       err,
			Latency:   time.Since(start),
		})
	}
	return out
}

func (c *qoderCatalog) models() []backend.Model {
	names := make([]string, 0, len(c.keys))
	for name := range c.keys {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]backend.Model, 0, len(names))
	for _, name := range names {
		out = append(out, backend.Model{ID: name, Name: name})
	}
	return out
}

func (e *QoderEngine) Models(ctx context.Context) ([]backend.Model, error) {
	return e.catalogFor(ctx).models(), nil
}

func (e *QoderEngine) HasModel(ctx context.Context, id string) bool {
	_, ok := e.catalogFor(ctx).keys[id]
	return ok
}

// --- Chat request body ---

type qoderChatContextText struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type qoderExtraModelConfig struct {
	IsReasoning bool   `json:"is_reasoning"`
	Key         string `json:"key"`
	IsVL        bool   `json:"is_vl,omitempty"`
}

type qoderOriginalContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type qoderChatContextExtra struct {
	ModelConfig     qoderExtraModelConfig `json:"modelConfig"`
	OriginalContent qoderOriginalContent  `json:"originalContent"`
}

type qoderChatContext struct {
	Extra qoderChatContextExtra `json:"extra"`
	Text  qoderChatContextText  `json:"text"`
}

type qoderModelConfig struct {
	Key         string `json:"key"`
	IsVL        bool   `json:"is_vl"`
	IsReasoning bool   `json:"is_reasoning"`
	Source      string `json:"source"`
}

type qoderBusiness struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	BeginAt int64  `json:"begin_at"`
}

type qoderChatBody struct {
	RequestID      string           `json:"request_id"`
	RequestSetID   string           `json:"request_set_id"`
	ChatRecordID   string           `json:"chat_record_id"`
	Stream         bool             `json:"stream"`
	ChatTask       string           `json:"chat_task"`
	ChatContext    qoderChatContext `json:"chat_context"`
	SessionID      string           `json:"session_id"`
	Source         int              `json:"source"`
	Version        string           `json:"version"`
	AliyunUserType string           `json:"aliyun_user_type"`
	SessionType    string           `json:"session_type"`
	AgentID        string           `json:"agent_id"`
	TaskID         string           `json:"task_id"`
	ModelConfig    qoderModelConfig `json:"model_config"`
	Messages       []qoderMessage   `json:"messages"`
	Business       qoderBusiness    `json:"business"`
	Parameters     map[string]any   `json:"parameters"`
	Tools          []map[string]any `json:"tools,omitempty"`
}

type qoderResponseMetaUsage struct {
	PromptTokens            int `json:"prompt_tokens"`
	CompletionTokens        int `json:"completion_tokens"`
	TotalTokens             int `json:"total_tokens"`
	CompletionTokensDetails struct {
		ReasoningTokens int `json:"reasoning_tokens"`
	} `json:"completion_tokens_details"`
	PromptTokensDetails struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
}

type qoderResponseMeta struct {
	ID    string                 `json:"id"`
	Usage qoderResponseMetaUsage `json:"usage"`
}

type qoderContentPart struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

type qoderFunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type qoderWireToolCall struct {
	ID       string            `json:"id"`
	Type     string            `json:"type"`
	Function qoderFunctionCall `json:"function"`
}

type qoderMessage struct {
	Role                string              `json:"role"`
	Content             string              `json:"content"`
	ResponseMeta        qoderResponseMeta   `json:"response_meta"`
	ReasoningContentSig string              `json:"reasoning_content_signature"`
	Contents            []qoderContentPart  `json:"contents,omitempty"`
	ToolCalls           []qoderWireToolCall `json:"tool_calls,omitempty"`
	Name                string              `json:"name,omitempty"`
	ToolCallID          string              `json:"tool_call_id,omitempty"`
}

func qoderStructuredMessage(role, text string) qoderMessage {
	return qoderMessage{Role: role, Content: text}
}

func qoderUserMessage(text string) qoderMessage {
	return qoderMessage{
		Role:     "user",
		Contents: []qoderContentPart{{Type: "text", Text: text}},
	}
}

func qoderWireToolCalls(calls []backend.ToolCall) []qoderWireToolCall {
	out := make([]qoderWireToolCall, 0, len(calls))
	for _, tc := range calls {
		if tc.Name == "" && tc.Arguments == "" {
			continue
		}
		typ := tc.Type
		if typ == "" {
			typ = "function"
		}
		out = append(out, qoderWireToolCall{
			ID:       tc.ID,
			Type:     typ,
			Function: qoderFunctionCall{Name: tc.Name, Arguments: tc.Arguments},
		})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func qoderToolCallMaps(calls []backend.ToolCall) []map[string]any {
	out := make([]map[string]any, 0, len(calls))
	for _, tc := range calls {
		typ := tc.Type
		if typ == "" {
			typ = "function"
		}
		out = append(out, map[string]any{
			"id":       tc.ID,
			"type":     typ,
			"function": map[string]any{"name": tc.Name, "arguments": tc.Arguments},
		})
	}
	return out
}

func qoderRenderToolCalls(calls []backend.ToolCall) string {
	b, _ := json.Marshal(qoderToolCallMaps(calls))
	return "Tool calls:\n" + string(b)
}

func qoderRenderToolResult(name, toolCallID, text string) string {
	var b strings.Builder
	b.WriteString("Tool result")
	if name != "" {
		b.WriteString(" (" + name + ")")
	}
	if toolCallID != "" {
		b.WriteString(" [" + toolCallID + "]")
	}
	if strings.TrimSpace(text) != "" {
		b.WriteString(":\n" + text)
	}
	return b.String()
}

func qoderSummarizeUnresolved(calls []backend.ToolCall) string {
	limit := len(calls)
	if limit > 6 {
		limit = 6
	}
	names := make([]string, 0, limit)
	for i := 0; i < limit; i++ {
		name := calls[i].Name
		if name == "" {
			name = "unknown"
		}
		names = append(names, name)
	}
	s := "Previously planned but unexecuted tool calls"
	if len(names) > 0 {
		s += ": " + strings.Join(names, ", ")
	}
	if len(calls) > limit {
		s += fmt.Sprintf(" and %d more", len(calls)-limit)
	}
	return s + "."
}

func qoderJoinSections(first, second string) string {
	if strings.TrimSpace(first) == "" {
		return second
	}
	if strings.TrimSpace(second) == "" {
		return first
	}
	return first + "\n\n" + second
}

func qoderHasResolvedToolResponse(msgs []backend.ChatMessage, idx int) bool {
	m := msgs[idx]
	if m.Role != "assistant" || len(m.ToolCalls) == 0 {
		return false
	}
	for i := idx + 1; i < len(msgs); i++ {
		switch msgs[i].Role {
		case "tool":
			return true
		case "assistant", "user", "system", "developer":
			return false
		}
	}
	return false
}

func qoderConvertMessage(m backend.ChatMessage, toolsEnabled, allowStructured bool) *qoderMessage {
	role := m.Role
	if role == "" {
		role = "user"
	}
	if role == "developer" {
		role = "system"
	}
	text := m.Content

	if role == "assistant" && toolsEnabled && len(m.ToolCalls) > 0 {
		if allowStructured {
			msg := qoderStructuredMessage("assistant", text)
			if strings.HasPrefix(strings.TrimSpace(text), "Tool calls:") {
				msg.Content = ""
			}
			msg.ToolCalls = qoderWireToolCalls(m.ToolCalls)
			return &msg
		}
		return ptr(qoderStructuredMessage("assistant", qoderSummarizeUnresolved(m.ToolCalls)))
	}
	if !toolsEnabled && role == "assistant" && len(m.ToolCalls) > 0 {
		text = qoderJoinSections(text, qoderRenderToolCalls(m.ToolCalls))
	}
	if role == "tool" {
		if toolsEnabled {
			msg := qoderStructuredMessage("tool", text)
			msg.Name = m.Name
			msg.ToolCallID = m.ToolCallID
			return &msg
		}
		role = "user"
		text = qoderRenderToolResult(m.Name, m.ToolCallID, text)
	}
	if strings.TrimSpace(text) == "" {
		return nil
	}
	if role == "user" {
		msg := qoderUserMessage(text)
		return &msg
	}
	msg := qoderStructuredMessage(role, text)
	return &msg
}

func qoderBuildMessages(msgs []backend.ChatMessage, prompt string, toolsEnabled bool) []qoderMessage {
	rebuilt := []qoderMessage{}
	for i, m := range msgs {
		allowStructured := toolsEnabled && qoderHasResolvedToolResponse(msgs, i)
		if converted := qoderConvertMessage(m, toolsEnabled, allowStructured); converted != nil {
			rebuilt = append(rebuilt, *converted)
		}
	}
	if len(rebuilt) == 0 && strings.TrimSpace(prompt) != "" {
		rebuilt = append(rebuilt, qoderUserMessage(prompt))
	}
	return rebuilt
}

func qoderLatestUserPrompt(msgs []backend.ChatMessage) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "user" && strings.TrimSpace(msgs[i].Content) != "" {
			return msgs[i].Content
		}
	}
	return ""
}

func qoderTruncateRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

func ptr[T any](v T) *T { return &v }

type qoderChatPlan struct {
	modelKey  string
	messages  []qoderMessage
	prompt    string
	params    map[string]any
	tools     []map[string]any
	reasoning bool
}

func qoderPlanChat(req *backend.ChatRequest, modelKey string, catalog *qoderCatalog, toolsEnabled bool) *qoderChatPlan {
	prompt := qoderLatestUserPrompt(req.Messages)
	params := map[string]any{}
	maxTokens := catalog.maxOutput(modelKey)
	if req.MaxTokens != nil {
		maxTokens = *req.MaxTokens
	}
	params["max_tokens"] = maxTokens
	if req.ToolChoice != nil {
		params["tool_choice"] = req.ToolChoice
	}
	if req.Temperature != nil {
		params["temperature"] = *req.Temperature
	}
	var tools []map[string]any
	for _, t := range req.Tools {
		fn := map[string]any{"name": t.Name}
		if t.Description != "" {
			fn["description"] = t.Description
		}
		if t.Parameters != nil {
			fn["parameters"] = t.Parameters
		}
		tools = append(tools, map[string]any{"type": "function", "function": fn})
	}
	return &qoderChatPlan{
		modelKey:  modelKey,
		messages:  qoderBuildMessages(req.Messages, prompt, toolsEnabled),
		prompt:    prompt,
		params:    params,
		tools:     tools,
		reasoning: catalog.reasoning(modelKey),
	}
}

func (p *qoderChatPlan) body(userType string) *qoderChatBody {
	nid := backend.UUID()
	return &qoderChatBody{
		RequestID:    nid,
		RequestSetID: backend.UUID(),
		ChatRecordID: nid,
		Stream:       true,
		ChatTask:     "FREE_INPUT",
		ChatContext: qoderChatContext{
			Extra: qoderChatContextExtra{
				ModelConfig:     qoderExtraModelConfig{IsReasoning: p.reasoning, Key: p.modelKey},
				OriginalContent: qoderOriginalContent{Type: "text", Text: p.prompt},
			},
			Text: qoderChatContextText{Type: "text", Text: p.prompt},
		},
		SessionID:      backend.UUID(),
		Source:         1,
		Version:        "3",
		AliyunUserType: userType,
		SessionType:    "qodercli",
		AgentID:        "agent_common",
		TaskID:         "common",
		ModelConfig:    qoderModelConfig{Key: p.modelKey, IsReasoning: p.reasoning, Source: "system"},
		Messages:       p.messages,
		Business:       qoderBusiness{ID: backend.UUID(), Name: qoderTruncateRunes(p.prompt, 30), BeginAt: time.Now().UnixMilli()},
		Parameters:     p.params,
		Tools:          p.tools,
	}
}

// --- SSE stream handling ---

type qoderUsage struct {
	prompt     int
	completion int
	total      int
	reasoning  int
	cached     int
}

type qoderToolCallDelta struct {
	index     *int
	id        string
	typ       string
	name      string
	arguments string
}

type qoderFrameDelta struct {
	content      string
	toolCalls    []qoderToolCallDelta
	finishReason string
	usage        *qoderUsage
}

type qoderStreamEvent struct {
	done  bool
	delta *qoderFrameDelta
	busy  *qoderBusyError
	auth  *qoderAuthError
	err   error
}

func qoderParseBody(obj map[string]any) map[string]any {
	body, ok := obj["body"]
	if !ok {
		return nil
	}
	switch b := body.(type) {
	case map[string]any:
		return b
	case string:
		var parsed map[string]any
		if json.Unmarshal([]byte(b), &parsed) == nil {
			return parsed
		}
	}
	return nil
}

func qoderBodyMessage(obj map[string]any, bodyMap map[string]any) string {
	if bodyMap != nil {
		if msg, _ := bodyMap["message"].(string); msg != "" {
			return msg
		}
		if code := bodyMap["code"]; code != nil {
			if s := qoderCodeString(code); s != "" {
				return s
			}
		}
	}
	if s, _ := obj["body"].(string); s != "" {
		return qoderTruncateBody([]byte(s))
	}
	if s, _ := obj["statusCode"].(string); s != "" {
		return s
	}
	return "unknown error"
}

func qoderParseInner(inner map[string]any) *qoderFrameDelta {
	d := &qoderFrameDelta{}
	if u, ok := inner["usage"].(map[string]any); ok {
		d.usage = &qoderUsage{
			prompt:     qoderInt(u["prompt_tokens"]),
			completion: qoderInt(u["completion_tokens"]),
			total:      qoderInt(u["total_tokens"]),
		}
		if det, ok := u["prompt_tokens_details"].(map[string]any); ok {
			d.usage.cached = qoderInt(det["cached_tokens"])
		}
		if det, ok := u["completion_tokens_details"].(map[string]any); ok {
			d.usage.reasoning = qoderInt(det["reasoning_tokens"])
		}
	}
	choices, _ := inner["choices"].([]any)
	for _, ch := range choices {
		choice, ok := ch.(map[string]any)
		if !ok {
			continue
		}
		if finish, _ := choice["finish_reason"].(string); finish != "" {
			d.finishReason = finish
		}
		deltaMap, _ := choice["delta"].(map[string]any)
		if c, _ := deltaMap["content"].(string); c != "" {
			d.content += c
		}
		tcList, _ := deltaMap["tool_calls"].([]any)
		for _, item := range tcList {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			var td qoderToolCallDelta
			if idx, ok := m["index"].(float64); ok {
				i := int(idx)
				td.index = &i
			}
			td.id, _ = m["id"].(string)
			td.typ, _ = m["type"].(string)
			if fn, ok := m["function"].(map[string]any); ok {
				td.name, _ = fn["name"].(string)
				td.arguments, _ = fn["arguments"].(string)
			}
			d.toolCalls = append(d.toolCalls, td)
		}
	}
	if d.content == "" && len(d.toolCalls) == 0 && d.finishReason == "" && d.usage == nil {
		return nil
	}
	return d
}

// qoderParseStreamLine classifies one SSE line: terminal marker, normal chat
// frame, or a gateway/auth/busy error.
func qoderParseStreamLine(line string) qoderStreamEvent {
	var ev qoderStreamEvent
	line = strings.TrimSpace(line)
	if line == "" {
		return ev
	}
	if strings.HasPrefix(line, "event:") {
		switch strings.TrimSpace(line[len("event:"):]) {
		case "finish":
			ev.done = true
		case "error":
			ev.err = errors.New("qoder upstream stream failed (event:error)")
		}
		return ev
	}
	if !strings.HasPrefix(line, "data:") {
		return ev
	}
	payload := strings.TrimSpace(line[len("data:"):])
	if payload == "" {
		return ev
	}
	if payload == "[DONE]" {
		ev.done = true
		return ev
	}
	var obj map[string]any
	if json.Unmarshal([]byte(payload), &obj) != nil {
		return ev
	}
	bodyStr, _ := obj["body"].(string)
	if bodyStr == "[DONE]" {
		ev.done = true
		return ev
	}
	bodyMap := qoderParseBody(obj)

	scv, _ := obj["statusCodeValue"].(float64)
	if scv >= 400 {
		if scv == 401 || scv == 403 {
			if bodyMap != nil && qoderCodeString(bodyMap["code"]) == qoderBusyCode {
				busy := &qoderBusyError{status: int(scv)}
				if msg, _ := bodyMap["message"].(string); msg != "" {
					busy.message = msg
					busy.retryAfter = qoderBusyRetryAfter(msg)
				}
				ev.busy = busy
				return ev
			}
			ev.auth = &qoderAuthError{status: int(scv), detail: qoderBodyMessage(obj, bodyMap)}
			return ev
		}
		ev.err = fmt.Errorf("qoder gateway error (%d): %s", int(scv), qoderBodyMessage(obj, bodyMap))
		return ev
	}
	if bodyMap != nil && qoderCodeString(bodyMap["code"]) == "105" {
		ev.auth = &qoderAuthError{status: 401, detail: qoderBodyMessage(obj, bodyMap)}
		return ev
	}
	if bodyStr == "" {
		if success, has := obj["success"].(bool); has && !success {
			msg := qoderString(obj["message"])
			if msg == "" {
				msg = qoderString(obj["msgInfo"])
			}
			code, _ := obj["msgCode"].(float64)
			if msg == "" && code == 0 {
				return ev
			}
			if msg == "" {
				msg = fmt.Sprintf("msgCode %d", int(code))
			}
			ev.err = fmt.Errorf("qoder gateway error: %s", msg)
		}
		return ev
	}
	var inner map[string]any
	if json.Unmarshal([]byte(bodyStr), &inner) != nil {
		return ev
	}
	ev.delta = qoderParseInner(inner)
	return ev
}

type qoderToolAccumulator struct {
	calls []backend.ToolCall
}

func (a *qoderToolAccumulator) append(deltas []qoderToolCallDelta) {
	for _, d := range deltas {
		if len(a.calls) >= qoderMaxToolCalls {
			return
		}
		i := len(a.calls)
		if d.index != nil {
			i = *d.index
		}
		if i < 0 || i > len(a.calls) || i >= qoderMaxToolCalls {
			i = len(a.calls)
		}
		for len(a.calls) <= i {
			a.calls = append(a.calls, backend.ToolCall{Type: "function"})
		}
		c := &a.calls[i]
		if d.id != "" {
			c.ID = d.id
		}
		if d.typ != "" {
			c.Type = d.typ
		}
		if d.name != "" {
			c.Name = d.name
		}
		if d.arguments != "" {
			c.Arguments += d.arguments
		}
	}
}

func (a *qoderToolAccumulator) snapshot() []backend.ToolCall {
	out := make([]backend.ToolCall, len(a.calls))
	copy(out, a.calls)
	for i := range out {
		if out[i].ID == "" {
			out[i].ID = backend.NewID("call_")
		}
		if out[i].Type == "" {
			out[i].Type = "function"
		}
		if out[i].Arguments == "" {
			out[i].Arguments = "{}"
		}
	}
	return out
}

func qoderMapUsage(u *qoderUsage) backend.Usage {
	total := u.total
	if total == 0 {
		total = u.prompt + u.completion
	}
	return backend.Usage{
		InputTokens:     u.prompt,
		OutputTokens:    u.completion,
		ThinkingTokens:  u.reasoning,
		CacheReadTokens: u.cached,
		TotalTokens:     total,
	}
}

func (e *QoderEngine) Complete(ctx context.Context, model, prompt string) (*backend.Result, error) {
	res, err := e.Chat(ctx, &backend.ChatRequest{Model: model, Messages: []backend.ChatMessage{{Role: "user", Content: prompt}}}, nil)
	if err != nil {
		return nil, err
	}
	return &backend.Result{Response: res.Content, Usage: res.Usage, Status: "SUCCESS"}, nil
}

func (e *QoderEngine) Stream(ctx context.Context, model, prompt string, onDelta func(string)) (*backend.Result, error) {
	var full strings.Builder
	res, err := e.Chat(ctx, &backend.ChatRequest{Model: model, Messages: []backend.ChatMessage{{Role: "user", Content: prompt}}, Stream: true}, func(d backend.ChatDelta) {
		if d.Content == "" {
			return
		}
		full.WriteString(d.Content)
		if onDelta != nil {
			onDelta(d.Content)
		}
	})
	if err != nil {
		return nil, err
	}
	return &backend.Result{Response: full.String(), Usage: res.Usage, Status: "SUCCESS"}, nil
}

func (e *QoderEngine) Chat(ctx context.Context, req *backend.ChatRequest, onDelta func(backend.ChatDelta)) (*backend.ChatResult, error) {
	if backend.HasImages(req.Messages) {
		return nil, fmt.Errorf("qoder backend does not support image content")
	}
	model := req.Model
	if model == "" {
		model = e.Default()
	}
	catalog := e.catalogFor(ctx)
	modelKey, ok := catalog.keys[model]
	if !ok {
		return nil, fmt.Errorf("unsupported qoder model %q; supported: %s", model, catalog.supported())
	}
	toolsEnabled := len(req.Tools) > 0
	plan := qoderPlanChat(req, modelKey, catalog, toolsEnabled)

	auths, start := e.snapshotAuths()
	if len(auths) == 0 {
		return nil, fmt.Errorf("no qoder accounts")
	}
	e.mu.Lock()
	e.next++
	e.mu.Unlock()

	var lastErr error
	for i := range auths {
		a := auths[(start+i)%len(auths)]
		acct := e.accountFor(a)
		res, started, err := e.chatOnce(ctx, acct, plan, onDelta)
		e.trackQuota(a, err)
		if err == nil {
			return res, nil
		}
		lastErr = err
		if started || ctx.Err() != nil {
			return nil, err
		}
		if !qoderRotatable(err) {
			return nil, err
		}
		slog.Warn("qoder account failed; rotating", "err", err)
	}
	return nil, lastErr
}

// chatOnce performs one account's request, refreshing the session once on
// auth errors and backing off once on gateway-busy rejections.
func (e *QoderEngine) chatOnce(ctx context.Context, acct *qoderAccount, plan *qoderChatPlan, onDelta func(backend.ChatDelta)) (*backend.ChatResult, bool, error) {
	for attempt := 1; ; attempt++ {
		sess, err := e.ensureSession(ctx, acct)
		if err != nil {
			return nil, false, err
		}
		body, err := qoderMarshalNoEscape(plan.body(sess.UserType))
		if err != nil {
			return nil, false, err
		}
		res, started, err := e.chatStreamOnce(ctx, acct, sess, plan.modelKey, qoderEncode(body), onDelta)
		if err == nil {
			return res, started, nil
		}
		if started || ctx.Err() != nil {
			return nil, started, err
		}
		var busy *qoderBusyError
		if errors.As(err, &busy) {
			if attempt >= 2 {
				return nil, false, err
			}
			wait := busy.retryAfter
			if wait <= 0 || wait > 10*time.Second {
				wait = qoderBusyBackoff
			}
			slog.Warn("qoder gateway busy; backing off", "wait", wait)
			select {
			case <-ctx.Done():
				return nil, false, ctx.Err()
			case <-time.After(wait):
			}
			continue
		}
		var authErr *qoderAuthError
		if errors.As(err, &authErr) {
			if attempt >= 2 {
				return nil, false, err
			}
			slog.Warn("qoder auth error before content; refreshing session", "err", err)
			if _, rerr := e.renewSession(ctx, acct, true); rerr != nil {
				return nil, false, err
			}
			continue
		}
		return nil, false, err
	}
}

func (e *QoderEngine) chatStreamOnce(ctx context.Context, acct *qoderAccount, sess *qoderSession, modelKey, bodyEnc string, onDelta func(backend.ChatDelta)) (*backend.ChatResult, bool, error) {
	urlStr := e.chatBase(acct.region) + "/algo/api/v2/service/pro/sse/agent_chat_generation?FetchKeys=llm_model_result&AgentId=agent_common&Encode=1"
	date := strconv.FormatInt(time.Now().Unix(), 10)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, urlStr, strings.NewReader(bodyEnc))
	if err != nil {
		return nil, false, err
	}
	req.Header = qoderSessionHeaders(sess, date, sess.bearer(date, bodyEnc, qoderSigPath(urlStr)), "text/event-stream")
	req.Header.Set("cache-control", "no-cache")
	req.Header.Set("x-model-key", modelKey)
	req.Header.Set("x-model-source", "system")

	resp, err := e.client().Do(req)
	if err != nil {
		return nil, false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
		return nil, false, qoderAPIError(resp.StatusCode, detail)
	}

	var content strings.Builder
	acc := &qoderToolAccumulator{}
	started := false
	sawDone := false
	finishReason := ""
	var usage *qoderUsage
	var gwErr error

	reader := bufio.NewReaderSize(resp.Body, 64<<10)
	for {
		lineBytes, readErr := reader.ReadBytes('\n')
		if len(lineBytes) > 0 {
			ev := qoderParseStreamLine(string(lineBytes))
			switch {
			case ev.busy != nil:
				return nil, started, ev.busy
			case ev.auth != nil:
				return nil, started, ev.auth
			case ev.err != nil:
				if gwErr == nil {
					gwErr = ev.err
				}
			}
			if ev.done {
				sawDone = true
			}
			if d := ev.delta; d != nil {
				if d.usage != nil {
					usage = d.usage
				}
				if d.content != "" {
					content.WriteString(d.content)
					started = true
					if onDelta != nil {
						onDelta(backend.ChatDelta{Content: d.content})
					}
				}
				if len(d.toolCalls) > 0 {
					acc.append(d.toolCalls)
				}
				if d.finishReason != "" {
					finishReason = d.finishReason
				}
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			if ctx.Err() != nil {
				return nil, started, ctx.Err()
			}
			return nil, started, readErr
		}
	}
	if !sawDone {
		if gwErr != nil {
			return nil, started, gwErr
		}
		return nil, started, fmt.Errorf("qoder stream ended without [DONE] (connection truncated?)")
	}

	calls := acc.snapshot()
	if len(calls) > 0 && onDelta != nil {
		onDelta(backend.ChatDelta{ToolCalls: calls})
	}
	finalReason := finishReason
	if len(calls) > 0 {
		finalReason = "tool_calls"
	} else if finalReason == "" {
		finalReason = "stop"
	}
	res := &backend.ChatResult{
		Content:      content.String(),
		ToolCalls:    calls,
		FinishReason: finalReason,
	}
	if usage != nil {
		res.Usage = qoderMapUsage(usage)
	}
	return res, started, nil
}

// --- Login ---

func (e *QoderEngine) Login(ctx context.Context) error {
	in := e.Stdin
	if in == nil {
		in = os.Stdin
	}
	out := e.Out
	if out == nil {
		out = os.Stdout
	}
	reader := bufio.NewReader(in)

	regionName := qoderRegionFor(e.Region).Name
	if strings.TrimSpace(e.Region) == "" {
		fmt.Fprint(out, "Region [global/cn] (default global): ")
		line, _ := reader.ReadString('\n')
		regionName = qoderRegionFor(strings.TrimSpace(line)).Name
	}

	pat := strings.TrimSpace(os.Getenv("QODER_PAT"))
	if pat == "" {
		fmt.Fprint(out, "Qoder personal access token (pt-...): ")
		line, err := reader.ReadString('\n')
		pat = strings.TrimSpace(line)
		if pat == "" && err != nil {
			return err
		}
	}
	if pat == "" {
		return fmt.Errorf("no Qoder PAT provided")
	}
	if !strings.HasPrefix(pat, "pt-") {
		fmt.Fprintln(out, "note: Qoder PATs usually start with \"pt-\"")
	}

	region := qoderRegionFor(regionName)
	acct := &qoderAccount{auth: &auth.Auth{Provider: "qoder", AccessToken: pat, Region: region.Name}, region: region}
	acct.machineID, acct.machineToken, acct.machineType = qoderMachineIdentity(pat)

	jt, err := e.requestJobToken(ctx, acct, "", "", false)
	if err != nil {
		return fmt.Errorf("qoder login failed: %w", err)
	}
	name := qoderString(jt["name"])
	id := qoderString(jt["id"])
	tier := e.fetchUserType(ctx, acct, id)
	if tier == "" {
		tier = qoderString(jt["userType"])
	}
	fmt.Fprintf(out, "Signed in to Qoder (%s) as %s [id=%s tier=%s]\n", region.Name, name, id, tier)

	return e.Store.Save([]*auth.Auth{{
		Provider:    "qoder",
		AccessToken: pat,
		AccountID:   id,
		Email:       name,
		Tier:        tier,
		Region:      region.Name,
	}})
}

// --- Quota ---

func (e *QoderEngine) Quota() auth.ProviderQuota {
	auths, _ := e.snapshotAuths()
	e.quotaMu.Lock()
	limited := make(map[*auth.Auth]time.Time, len(e.quotaErr))
	for a, t := range e.quotaErr {
		limited[a] = t
	}
	e.quotaMu.Unlock()
	q := auth.ProviderQuota{Provider: "qoder", DefaultModel: e.Default(), Accounts: make([]auth.AccountQuota, 0, len(auths))}
	for _, a := range auths {
		q.Accounts = append(q.Accounts, auth.AccountStatus("qoder", a, limited))
	}
	return q
}
