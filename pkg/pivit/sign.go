package pivit

import (
	"bytes"
	"crypto/x509"
	"encoding/pem"
	"io"
	"os"
	"strings"

	"github.com/cashapp/pivit/pkg/pivit/status"
	"github.com/cashapp/pivit/pkg/pivit/utils"
	"github.com/cashapp/pivit/pkg/pivit/yubikey"
	cms "github.com/github/smimesign/ietf-cms"
	"github.com/pkg/errors"
)

type SignOpts struct {
	StatusFd           int
	Detach             bool
	Armor              bool
	UserId             string
	TimestampAuthority string
	FileArgs           []string
}

// Sign signs the filename given in fileArgs or the content from stdin if no filename was supplied
func Sign(slot string, opts *SignOpts) error {
	yk, err := yubikey.GetSigner(slot)
	if err != nil {
		return errors.Wrap(err, "open PIV for signing")
	}

	pivSlot := utils.GetSlot(slot)
	cert, err := yk.Certificate(pivSlot)
	if err != nil {
		return errors.Wrap(err, "get identity certificate")
	}

	if err = certificateContainsUserId(cert, opts.UserId); err != nil {
		return errors.Wrap(err, "no suitable certificate found")
	}

	yubikeySigner := yubikey.NewYubikeySigner(yk, pivSlot)
	status.SetupStatus(opts.StatusFd)
	var f io.ReadCloser
	if len(opts.FileArgs) == 1 {
		if f, err = os.Open(opts.FileArgs[0]); err != nil {
			return errors.Wrapf(err, "open message file (%s)", opts.FileArgs[0])
		}
		defer func() {
			_ = f.Close()
		}()
	} else {
		f = os.Stdin
	}

	dataBuf := new(bytes.Buffer)
	if _, err = io.Copy(dataBuf, f); err != nil {
		return errors.Wrap(err, "read message to sign")
	}

	sd, err := cms.NewSignedData(dataBuf.Bytes())
	if err != nil {
		return errors.Wrap(err, "create signed data")
	}

	if err = sd.Sign([]*x509.Certificate{cert}, yubikeySigner); err != nil {
		return errors.Wrap(err, "sign message")
	}
	// Git is looking for "\n[GNUPG:] SIG_CREATED ", meaning we need to print a
	// line before SIG_CREATED. BEGIN_SIGNING seems appropriate. GPG emits this,
	// though GPGSM does not.
	status.EmitBeginSigning()
	if opts.Detach {
		sd.Detached()
	}

	if len(opts.TimestampAuthority) > 0 {
		if err = sd.AddTimestamps(opts.TimestampAuthority); err != nil {
			return errors.Wrap(err, "add timestamp to signature")
		}
	}

	chain := []*x509.Certificate{cert}
	if err = sd.SetCertificates(chain); err != nil {
		return errors.Wrap(err, "set certificates in signature")
	}

	der, err := sd.ToDER()
	if err != nil {
		return errors.Wrap(err, "serialize signature")
	}

	status.EmitSigCreated(cert, opts.Detach)
	if opts.Armor {
		err = pem.Encode(os.Stdout, &pem.Block{
			Type:  "SIGNED MESSAGE",
			Bytes: der,
		})
	} else {
		_, err = os.Stdout.Write(der)
	}
	if err != nil {
		return errors.New("write signature")
	}

	return nil
}

func certificateContainsUserId(cert *x509.Certificate, userId string) error {
	email, err := normalizeEmail(userId)
	if err != nil {
		fingerprint := normalizeFingerprint(userId)
		if !strings.EqualFold(utils.CertHexFingerprint(cert), fingerprint) {
			return errors.Errorf("no certificate found with fingerprint %s", fingerprint)
		}
	} else {
		if !certificateContainsEmail(cert, email) {
			return errors.Errorf("no certificate found with email %s", email)
		}
	}

	return nil
}

// normalizeEmail extracts the email address portion from the user ID string
// or an error if the user ID string doesn't contain a valid email address.
//
// The user ID string is expected to be either:
// - An email address
// - A string containing a name, comment and email address, like "Full Name (comment) <email@example.com>"
// - A hex fingerprint
func normalizeEmail(userId string) (string, error) {
	emailStartIndex := strings.Index(userId, "<")
	if emailStartIndex != -1 {
		emailEndIndex := strings.Index(userId, ">")
		return userId[emailStartIndex:emailEndIndex], nil
	}

	if strings.ContainsRune(userId, '@') {
		return userId, nil
	}

	return "", errors.New("user id doesn't contain email address")
}

func normalizeFingerprint(userId string) string {
	return strings.TrimPrefix(userId, "0x")
}

func certificateContainsEmail(certificate *x509.Certificate, email string) bool {
	for _, sanEmail := range certificate.EmailAddresses {
		if sanEmail == email {
			return true
		}
	}

	return false
}
