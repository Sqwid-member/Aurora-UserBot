package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/Sqwid-member/Aurora-UserBot/internal/paths"
	"github.com/Sqwid-member/Aurora-UserBot/internal/tgc"
	"github.com/Sqwid-member/Aurora-UserBot/internal/web"
)

// ensureCore returns a client for the local Aurora core, starting it when
// ask is true. It waits until the panel answers, so the caller never has to
// race the startup.
func ensureCore(layout paths.Layout, ask bool) (*daemonClient, error) {
	client, err := newDaemonClient(layout)
	if err != nil {
		return nil, err
	}
	if client.isAlive() {
		return client, nil
	}
	if !ask {
		return nil, errors.New("ядро Aurora не запущено (aurora start)")
	}

	fmt.Println("→ Запускаю ядро Aurora…")
	if err := cmdStart(layout); err != nil && !strings.Contains(err.Error(), "вже працює") {
		return nil, err
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if client.isAlive() {
			return client, nil
		}
		time.Sleep(250 * time.Millisecond)
	}
	return nil, errors.New("ядро Aurora не відповідає — подивіться 'aurora logs'")
}

// waitForConnected blocks until the core reports a live Telegram link.
func waitForConnected(client *daemonClient, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		st, err := client.getAuth()
		if err == nil && (st.Connected || st.SignedIn) {
			return nil
		}
		time.Sleep(250 * time.Millisecond)
	}
	return errors.New("ядро не підключилося до Telegram — перевірте мережу та 'aurora logs'")
}

// waitSignedIn polls until the core confirms the session is authorized.
func waitSignedIn(client *daemonClient, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if st, err := client.getAuth(); err == nil && st.SignedIn && st.Session == "authorized" {
			return true
		}
		time.Sleep(250 * time.Millisecond)
	}
	st, err := client.getAuth()
	return err == nil && st.SignedIn
}

// isSignupNeeded reports whether the error means the number has no Telegram
// account yet and must be registered.
func isSignupNeeded(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "не зареєстровано") ||
		strings.Contains(msg, "not registered") ||
		strings.Contains(msg, "sign-up required")
}

// isPasswordNeeded reports whether the error is Telegram's 2FA challenge.
func isPasswordNeeded(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToUpper(err.Error())
	return strings.Contains(msg, "SESSION_PASSWORD_NEEDED") ||
		strings.Contains(msg, "PASSWORD_AUTH_NEEDED") ||
		strings.Contains(msg, "2FA")
}

// isFatalLoginErr reports errors that cannot be fixed by retrying the same
// input (bad API keys, banned number, no connectivity to the core).
func isFatalLoginErr(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToUpper(err.Error())
	for _, k := range []string{"API_ID_INVALID", "API_HASH_INVALID", "PHONE_NUMBER_BANNED", "не запущено", "не відповідає"} {
		if strings.Contains(msg, k) {
			return true
		}
	}
	return false
}

// ask reads one line from stdin with the given prompt.
func ask(prompt string) (string, error) {
	fmt.Print(prompt)
	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil && line == "" {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

// cmdQRLogin logs in by QR: the already authorized Telegram app approves the
// session, so no phone number, SMS or 2FA code is needed.
//
// On a single phone (Termux) nothing has to be scanned: the tg:// login link
// is tappable right in the terminal, and we also try to open it via
// termux-open so Telegram itself asks for confirmation.
func cmdQRLogin(layout paths.Layout) error {
	client, err := ensureCore(layout, true)
	if err != nil {
		return err
	}
	if st, err := client.getAuth(); err == nil && st.SignedIn {
		fmt.Printf("✓ Акаунт уже авторизовано (%s)\n", st.Phone)
		return nil
	}

	fmt.Println()
	fmt.Println("  \033[1;36m🔗 ВХІД ПО QR\033[0m")
	fmt.Println("  \033[2mбез номера телефону та SMS — сканувати не треба\033[0m")
	fmt.Println("  \033[2mТапніть посилання нижче на цьому ж телефоні,\033[0m")
	fmt.Println("  \033[2mTelegram сам попросить підтвердити вхід.\033[0m")
	fmt.Println("  \033[2mВідкривати ТІЛЬКИ офіційним Telegram: моди замість\033[0m")
	fmt.Println("  \033[2mпідтвердження показують сканер (обмеження модів).\033[0m")
	fmt.Println()

	go func() { _ = client.startQR() }()

	lastURL := ""
	deadline := time.Now().Add(3 * time.Minute)
	for time.Now().Before(deadline) {
		st, err := client.qrState()
		if err == nil {
			if st.URL != "" && st.URL != lastURL {
				lastURL = st.URL
				fmt.Printf("  Відкрийте це посилання в Telegram (тапніть його):\n\n  \033[1;36m%s\033[0m\n\n", st.URL)
				fmt.Printf("  (діє до %s; те саме є у веб-панелі: вкладка «QR на цьому телефоні»)\n", st.Expires.Format("15:04:05"))
				// Best effort: on Termux this fires an intent that opens
				// Telegram on the approval screen. Harmless elsewhere.
				openBrowserCLI(st.URL)
			}
			if st.Running {
				fmt.Print("  ⏳ чекаю підтвердження в Telegram…")
				if waitSignedIn(client, 150*time.Second) {
					fmt.Print("\r  ✓ підтверджено                                   \n")
					fmt.Println("\n\033[1;32m🎉 УСПІШНО АВТОРИЗОВАНО\033[0m")
					fmt.Println("  Сесію збережено, юзербот готовий до роботи.")
					fmt.Printf("  Панель: %s/?token=%s\n", client.baseURL, client.token)
					return nil
				}
				fmt.Print("\r  ✳ код оновлено                                  \n")
				continue
			}
		}
		time.Sleep(2 * time.Second)
	}
	return errors.New("qr-вхід не підтверджено вчасно")
}

// cmdTerminalLogin is the plain-line login wizard: phone → code → 2FA.
// It drives the running core over the panel API so the session file has a
// single writer no matter which front-end started the flow.
func cmdTerminalLogin(layout paths.Layout) error {
	client, err := ensureCore(layout, true)
	if err != nil {
		return err
	}

	if st, err := client.getAuth(); err == nil && st.SignedIn {
		fmt.Printf("✓ Акаунт уже авторизовано (%s)\n", st.Phone)
		ans, _ := ask("Бажаєте увійти з іншого номера? [y/N]: ")
		if !strings.EqualFold(ans, "y") && !strings.EqualFold(ans, "так") {
			return nil
		}
		if err := client.logout(); err != nil {
			return fmt.Errorf("не вдалося вийти: %w", err)
		}
		time.Sleep(500 * time.Millisecond)
	}

	fmt.Println()
	fmt.Println("  \033[1;36m🔑 ВХІД У TELEGRAM\033[0m")
	fmt.Println("  \033[2mномер → код із Telegram → пароль 2FA (якщо є)\033[0m")
	fmt.Println()

	if err := waitForConnected(client, 45*time.Second); err != nil {
		return err
	}
	if st, err := client.getAuth(); err == nil && st.SignedIn {
		fmt.Println("✓ Акаунт уже авторизовано")
		return nil
	}

	// Step 1 — phone.
	var phone string
	for {
		raw, err := ask("  Номер телефону (+380…, +48…): ")
		if err != nil {
			return errors.New("ввід перервано")
		}
		if strings.EqualFold(raw, "s") {
			// Re-send the code over SMS, needed when the app channel is dead.
			raw, err = ask("  Номер телефону для SMS: ")
			if err != nil {
				return errors.New("ввід перервано")
			}
			phone = tgc.CleanPhone(raw)
			if phone == "" {
				fmt.Println("  ✖ Введіть номер у міжнародному форматі")
				continue
			}
			fmt.Printf("  → Надсилаю код по SMS на \033[1m%s\033[0m…", phone)
			if err := client.requestCodeSMS(phone); err != nil {
				fmt.Printf("\r  ✖ %v\n", err)
				if isFatalLoginErr(err) {
					return err
				}
				continue
			}
			fmt.Print("\r  ✓ Код надіслано по SMS                            \n")
			break
		}
		phone = tgc.CleanPhone(raw)
		if phone == "" {
			fmt.Println("  ✖ Введіть номер у міжнародному форматі")
			continue
		}
		fmt.Printf("  → Надсилаю код на \033[1m%s\033[0m…", phone)
		if err := client.requestCode(phone); err != nil {
			fmt.Printf("\r  ✖ %v\n", err)
			if isFatalLoginErr(err) {
				return err
			}
			continue
		}
		fmt.Print("\r  ✓ Код надіслано                                    \n")
		break
	}
	fmt.Println("  \033[2mВведіть s, щоб надіслати код по SMS замість застосунку.\033[0m")
	fmt.Println("  \033[2mЯкщо код не прийшов — введіть r, щоб надіслати ще раз (зазвичай SMS або дзвінок).\033[0m")
	fmt.Println("  \033[2mЯкщо коду нема взагалі (акаунт ніде не відкрито) —\033[0m")
	fmt.Println("  \033[2mкраще вийдіть і виконайте 'aurora login qr': тап по посиланню на цьому ж телефоні.\033[0m")
	if st, err := client.getAuth(); err == nil && st.AppOnly {
		fmt.Println("  \033[1;33m⚠ Telegram віддав код лише в застосунок, SMS-каналу для сторонніх клієнтів нема.\033[0m")
		fmt.Println("  \033[2mВаріанти: 'aurora login qr' (тап на цьому телефоні) або імпорт StringSession у веб-панелі.\033[0m")
	}

	// Step 2 — code (and optional step 3 — 2FA).
	for {
		code, err := ask("  Код із Telegram: ")
		if err != nil {
			return errors.New("ввід перервано")
		}
		if code == "" {
			fmt.Println("  ✖ Порожній код")
			continue
		}
		if strings.EqualFold(code, "r") {
			fmt.Print("  → Надсилаю код ще раз…")
			if err := client.resendCode(); err != nil {
				fmt.Printf("\r  ✖ %v\n", err)
				continue
			}
			fmt.Print("\r  ✓ Код надіслано повторно                           \n")
			continue
		}
		fmt.Print("  → Перевіряю…")
		submitErr := client.submitCode(code)
		if submitErr == nil {
			fmt.Print("\r  ✓ Код прийнято                                     \n")
			break
		}
		if isSignupNeeded(submitErr) {
			fmt.Print("\r  🆕 Номер не зареєстровано — створюємо акаунт          \n")
			name, err := ask("  Ім'я (обов'язково): ")
			if err != nil {
				return errors.New("ввід перервано")
			}
			if strings.TrimSpace(name) == "" {
				fmt.Println("  ✖ Ім'я не може бути порожнім")
				continue
			}
			last, _ := ask("  Прізвище (або Enter): ")
			fmt.Print("  → Реєструю акаунт…")
			if err := client.signUp(name, last); err != nil {
				fmt.Printf("\r  ✖ %v\n", err)
				if isFatalLoginErr(err) {
					return err
				}
				continue
			}
			fmt.Print("\r  ✓ Акаунт створено                                  \n")
			break
		}
		if isPasswordNeeded(submitErr) {
			fmt.Print("\r  🔒 Потрібен пароль 2FA                              \n")
			pass, err := ask("  Пароль 2FA: ")
			if err != nil {
				return errors.New("ввід перервано")
			}
			if err := client.submitPassword(pass); err != nil {
				fmt.Printf("\r  ✖ %v\n", err)
				continue
			}
			fmt.Print("\r  ✓ Пароль прийнято                                  \n")
			break
		}
		fmt.Printf("\r  ✖ %v\n", submitErr)
		if isFatalLoginErr(submitErr) {
			return submitErr
		}
	}

	if waitSignedIn(client, 20*time.Second) {
		fmt.Println()
		fmt.Println("\033[1;32m🎉 УСПІШНО АВТОРИЗОВАНО\033[0m")
		fmt.Println("  Сесію збережено, юзербот готовий до роботи.")
		fmt.Printf("  Панель: %s/?token=%s\n", client.baseURL, client.token)
		return nil
	}
	return errors.New("сесія не підтвердилася — подивіться 'aurora logs'")
}

// authSummary renders a short status line for the plain-line UI.
func authSummary(st web.AuthState) string {
	if st.SignedIn {
		return "авторизовано"
	}
	if !st.Connected {
		return "підключення…"
	}
	return "стан: " + string(st.State)
}
