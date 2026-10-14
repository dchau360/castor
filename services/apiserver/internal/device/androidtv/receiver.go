package androidtv

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
)

// The player app castor installs on the TV; receiver/build.sh makes castor.apk from receiver/.
const (
	receiverPackage  = "com.stupside.castor.receiver"
	receiverActivity = receiverPackage + "/.PlayerActivity"
	receiverControl  = receiverPackage + ".CONTROL"
)

var (
	//go:embed receiver/castor.apk
	receiverAPK []byte

	//go:embed receiver/VERSION
	receiverVersionFile string
)

func receiverVersion() int {
	v, err := strconv.Atoi(strings.TrimSpace(receiverVersionFile))
	if err != nil {
		panic("receiver/VERSION is not a number: " + err.Error())
	}
	return v
}

var installedVersion = regexp.MustCompile(`versionCode=(\d+)`)

// ensureReceiver installs castor's player unless this very version already is.
func (s *session) ensureReceiver(ctx context.Context) error {
	out, err := s.run(ctx, "dumpsys package "+receiverPackage, nil)
	if err != nil {
		return err
	}
	if m := installedVersion.FindSubmatch(out); m != nil && string(m[1]) == strconv.Itoa(receiverVersion()) {
		return nil
	}
	slog.InfoContext(ctx, "installing castor's android tv player", "device", s.name)
	err = s.install(ctx)
	if outcome := installFailure(""); errors.As(err, &outcome) && outcome.replaceable() {
		// Another signing key or a newer build holds the package; castor's own player can just go.
		if _, err := s.run(ctx, "pm uninstall "+receiverPackage, nil); err != nil {
			return err
		}
		err = s.install(ctx)
	}
	return err
}

// installFailure is the package manager's own refusal, in its words.
type installFailure string

func (f installFailure) Error() string {
	return "the TV refused castor's player: " + string(f)
}

func (f installFailure) replaceable() bool {
	return strings.Contains(string(f), "INSTALL_FAILED_UPDATE_INCOMPATIBLE") ||
		strings.Contains(string(f), "INSTALL_FAILED_VERSION_DOWNGRADE")
}

// install streams the APK into the package manager.
func (s *session) install(ctx context.Context) error {
	out, err := s.run(ctx, fmt.Sprintf("cmd package install -r -S %d", len(receiverAPK)), receiverAPK)
	if err != nil {
		return fmt.Errorf("installing castor's player: %w", err)
	}
	if text := strings.TrimSpace(string(out)); !strings.Contains(text, "Success") {
		return installFailure(text)
	}
	return nil
}
