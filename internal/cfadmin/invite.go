package cfadmin

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// InvitePrefix starts every join code.
const InvitePrefix = "xmesh-join-"

// ValidInviteName reports whether name can be a node name, which is what join matches it against.
func ValidInviteName(name string) bool { return namePattern.MatchString(name) }

// InviteTTL is how long a join code stays usable.
const InviteTTL = 15 * time.Minute

const maxInvite = 8 << 10

var (
	namePattern        = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,62}$`)
	fingerprintPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
	hostPattern        = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$`)
)

// Invite is everything a new device needs to join a mesh over Cloudflare: its own tunnel, the mesh's
// Access credentials, and the one-time password that pairs it with the inviting device. It is a secret
// until it expires, and holds no Cloudflare API credential.
type Invite struct {
	Version     int    `json:"v"`
	Name        string `json:"name"`
	Admin       string `json:"admin"`
	Fingerprint string `json:"fp"`
	Password    string `json:"pw"`
	Hostname    string `json:"host"`
	TunnelToken string `json:"tunnel"`
	AccessID    string `json:"cid"`
	AccessKey   string `json:"cs"`
	Expires     int64  `json:"exp"`
}

func (i Invite) String() string   { return "Invite{name=" + i.Name + " secrets=<redacted>}" }
func (i Invite) GoString() string { return i.String() }

func (i Invite) Validate(now time.Time) error {
	switch {
	case i.Version != 1:
		return errors.New("加入码版本不受支持")
	case !namePattern.MatchString(i.Name):
		return errors.New("加入码里的设备名无效")
	case !hostPattern.MatchString(i.Admin) || !hostPattern.MatchString(i.Hostname):
		return errors.New("加入码里的主机名无效")
	case !fingerprintPattern.MatchString(i.Fingerprint):
		return errors.New("加入码里的设备指纹无效")
	case i.Password == "" || i.TunnelToken == "" || i.AccessID == "" || i.AccessKey == "":
		return errors.New("加入码缺少凭据")
	case !now.Before(time.Unix(i.Expires, 0)):
		return errors.New("加入码已过期，请让对方重新生成")
	}
	return nil
}

// Encode renders the code as one line that survives copy and paste.
func (i Invite) Encode() (string, error) {
	raw, err := json.Marshal(i)
	if err != nil {
		return "", err
	}
	return InvitePrefix + base64.RawURLEncoding.EncodeToString(raw), nil
}

// DecodeInvite parses and validates a join code; errors never echo its contents.
func DecodeInvite(code string, now time.Time) (Invite, error) {
	code = strings.TrimSpace(code)
	body, ok := strings.CutPrefix(code, InvitePrefix)
	if !ok || len(code) > maxInvite {
		return Invite{}, errors.New("不是有效的加入码")
	}
	raw, err := base64.RawURLEncoding.DecodeString(body)
	var i Invite
	if err != nil || json.Unmarshal(raw, &i) != nil {
		return Invite{}, errors.New("不是有效的加入码")
	}
	if err := i.Validate(now); err != nil {
		return Invite{}, err
	}
	return i, nil
}

// NewPassword makes a connection password for one invite.
func NewPassword() (string, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("无法生成连接密码：%w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
