package tgc

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gotd/td/telegram/uploader"
	"github.com/gotd/td/tg"

	"github.com/Sqwid-member/Aurora-UserBot/internal/proto"
)

// Limits mirror Telegram's own so the user gets a clear message instead of
// a cryptic API error.
const (
	maxFirstName = 64
	maxLastName  = 64
	maxAbout     = 70
	maxAvatar    = 5 << 20 // 5 MiB
)

// AuthSession is one active login shown in Settings → Devices.
type AuthSession struct {
	Hash            int64  `json:"hash"`
	Current         bool   `json:"current"`
	OfficialApp     bool   `json:"official_app"`
	PasswordPending bool   `json:"password_pending"`
	Device          string `json:"device"`
	Platform        string `json:"platform"`
	System          string `json:"system"`
	App             string `json:"app"`
	AppVersion      string `json:"app_version"`
	IP              string `json:"ip"`
	Country         string `json:"country"`
	Region          string `json:"region"`
	Created         int64  `json:"created"`
	Active          int64  `json:"active"`
}

// Authorizations lists every active session of this account, current first.
func (r *Runtime) Authorizations(ctx context.Context) ([]AuthSession, error) {
	r.mu.RLock()
	client := r.client
	r.mu.RUnlock()
	if client == nil {
		return nil, errors.New("ядро Telegram ще не запущено")
	}
	if err := r.waitConnected(ctx, 30*time.Second); err != nil {
		return nil, err
	}
	res, err := client.API().AccountGetAuthorizations(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]AuthSession, 0, len(res.Authorizations))
	for _, a := range res.Authorizations {
		out = append(out, AuthSession{
			Hash:            a.Hash,
			Current:         a.Current,
			OfficialApp:     a.OfficialApp,
			PasswordPending: a.PasswordPending,
			Device:          a.DeviceModel,
			Platform:        a.Platform,
			System:          a.SystemVersion,
			App:             a.AppName,
			AppVersion:      a.AppVersion,
			IP:              a.IP,
			Country:         a.Country,
			Region:          a.Region,
			Created:         int64(a.DateCreated),
			Active:          int64(a.DateActive),
		})
	}
	return out, nil
}

// ResetAuthorization terminates one session by hash. Terminating the
// current session logs this client out — the panel warns about it.
func (r *Runtime) ResetAuthorization(ctx context.Context, hash int64) (bool, error) {
	r.mu.RLock()
	client := r.client
	r.mu.RUnlock()
	if client == nil {
		return false, errors.New("ядро Telegram ще не запущено")
	}
	if err := r.waitConnected(ctx, 30*time.Second); err != nil {
		return false, err
	}
	ok, err := client.API().AccountResetAuthorization(ctx, hash)
	if err != nil {
		return false, translateTelegramErr(err)
	}
	return ok, nil
}

// UpdateProfile changes first name, last name and bio through the official
// account API and refreshes the cached self user.
func (r *Runtime) UpdateProfile(ctx context.Context, first, last, about string) (proto.User, error) {
	first = strings.TrimSpace(first)
	last = strings.TrimSpace(last)
	about = strings.TrimSpace(about)
	if utf8.RuneCountInString(first) == 0 || utf8.RuneCountInString(first) > maxFirstName {
		return proto.User{}, fmt.Errorf("ім'я: від 1 до %d символів", maxFirstName)
	}
	if utf8.RuneCountInString(last) > maxLastName {
		return proto.User{}, fmt.Errorf("прізвище: до %d символів", maxLastName)
	}
	if utf8.RuneCountInString(about) > maxAbout {
		return proto.User{}, fmt.Errorf("біографія: до %d символів", maxAbout)
	}
	r.mu.RLock()
	client := r.client
	r.mu.RUnlock()
	if client == nil {
		return proto.User{}, errors.New("ядро Telegram ще не запущено")
	}
	if err := r.waitConnected(ctx, 30*time.Second); err != nil {
		return proto.User{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	u, err := client.API().AccountUpdateProfile(ctx, &tg.AccountUpdateProfileRequest{
		FirstName: first,
		LastName:  last,
		About:     about,
	})
	if err != nil {
		return proto.User{}, translateTelegramErr(err)
	}
	return r.rememberMe(u)
}

// UpdateUsername changes the @username of the account.
func (r *Runtime) UpdateUsername(ctx context.Context, username string) (proto.User, error) {
	username = strings.TrimPrefix(strings.TrimSpace(username), "@")
	if !validUsername(username) {
		return proto.User{}, errors.New("юзернейм: 5–32 символи, латиниця, цифри та _, починається з літери")
	}
	r.mu.RLock()
	client := r.client
	r.mu.RUnlock()
	if client == nil {
		return proto.User{}, errors.New("ядро Telegram ще не запущено")
	}
	if err := r.waitConnected(ctx, 30*time.Second); err != nil {
		return proto.User{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	u, err := client.API().AccountUpdateUsername(ctx, username)
	if err != nil {
		return proto.User{}, translateTelegramErr(err)
	}
	return r.rememberMe(u)
}

// UploadAvatar uploads a JPEG/PNG profile photo (up to 5 MiB).
func (r *Runtime) UploadAvatar(ctx context.Context, name string, data []byte) (proto.User, error) {
	if len(data) == 0 || len(data) > maxAvatar {
		return proto.User{}, fmt.Errorf("фото: до %d МБ у JPEG/PNG", maxAvatar>>20)
	}
	if !isJPEG(data) && !isPNG(data) {
		return proto.User{}, errors.New("фото: тільки JPEG або PNG")
	}
	if strings.TrimSpace(name) == "" {
		name = "avatar.jpg"
	}
	r.mu.RLock()
	client := r.client
	r.mu.RUnlock()
	if client == nil {
		return proto.User{}, errors.New("ядро Telegram ще не запущено")
	}
	if err := r.waitConnected(ctx, 60*time.Second); err != nil {
		return proto.User{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	file, err := uploader.NewUploader(client.API()).Upload(ctx, uploader.NewUpload(name, bytes.NewReader(data), int64(len(data))))
	if err != nil {
		return proto.User{}, translateTelegramErr(err)
	}
	if _, err := client.API().PhotosUploadProfilePhoto(ctx, &tg.PhotosUploadProfilePhotoRequest{File: file}); err != nil {
		return proto.User{}, translateTelegramErr(err)
	}
	return r.refreshMe(ctx)
}

// FullProfile is the self profile with bio, for prefilling the panel form.
type FullProfile struct {
	User  proto.User `json:"user"`
	About string     `json:"about"`
}

// SelfProfile reads the full self profile (name, username, bio).
func (r *Runtime) SelfProfile(ctx context.Context) (FullProfile, error) {
	r.mu.RLock()
	client := r.client
	r.mu.RUnlock()
	if client == nil {
		return FullProfile{}, errors.New("ядро Telegram ще не запущено")
	}
	if err := r.waitConnected(ctx, 30*time.Second); err != nil {
		return FullProfile{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	full, err := client.API().UsersGetFullUser(ctx, &tg.InputUserSelf{})
	if err != nil {
		return FullProfile{}, err
	}
	fu := full.GetFullUser()
	about, _ := fu.GetAbout()
	for _, u := range full.GetUsers() {
		if user, ok := u.(*tg.User); ok && user.Self {
			return FullProfile{User: userProto(user), About: about}, nil
		}
	}
	// Self not in the users list (should not happen): bio at least.
	return FullProfile{About: about}, nil
}

// refreshMe re-reads the self user after a profile mutation.
func (r *Runtime) refreshMe(ctx context.Context) (proto.User, error) {
	r.mu.RLock()
	client := r.client
	r.mu.RUnlock()
	if client == nil {
		return proto.User{}, errors.New("ядро Telegram ще не запущено")
	}
	users, err := client.API().UsersGetUsers(ctx, []tg.InputUserClass{&tg.InputUserSelf{}})
	if err != nil {
		return proto.User{}, err
	}
	if len(users) == 0 {
		return proto.User{}, errors.New("Telegram не повернув профіль")
	}
	return r.rememberMe(users[0])
}

// rememberMe stores the self user from an update result and returns it.
func (r *Runtime) rememberMe(u tg.UserClass) (proto.User, error) {
	user, ok := u.(*tg.User)
	if !ok {
		return proto.User{}, fmt.Errorf("неочікуваний профіль %T", u)
	}
	me := userProto(user)
	r.mu.Lock()
	r.me = &me
	r.mu.Unlock()
	r.remember(proto.PeerInfo{ID: me.ID, Type: "user", Title: me.Display()})
	return me, nil
}

// userProto maps a raw Telegram user onto the plugin-facing shape.
func userProto(u *tg.User) proto.User {
	if u == nil {
		return proto.User{}
	}
	username, _ := u.GetUsername()
	return proto.User{
		ID:       u.ID,
		Username: username,
		First:    u.FirstName,
		Last:     u.LastName,
		Phone:    u.Phone,
		Bot:      u.Bot,
		Premium:  u.Premium,
	}
}

func validUsername(s string) bool {
	if len(s) < 5 || len(s) > 32 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		isLetter := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
		isDigit := c >= '0' && c <= '9'
		if i == 0 {
			if !isLetter {
				return false
			}
			continue
		}
		if !isLetter && !isDigit && c != '_' {
			return false
		}
	}
	return true
}

func isJPEG(p []byte) bool { return len(p) > 3 && p[0] == 0xFF && p[1] == 0xD8 && p[2] == 0xFF }

func isPNG(p []byte) bool {
	return len(p) > 8 && p[0] == 0x89 && p[1] == 'P' && p[2] == 'N' && p[3] == 'G'
}
