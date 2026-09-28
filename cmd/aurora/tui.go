package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/Sqwid-member/Aurora-UserBot/internal/buildinfo"
	"github.com/Sqwid-member/Aurora-UserBot/internal/paths"
	"github.com/Sqwid-member/Aurora-UserBot/internal/tgc"
)

func cmdTUI(layout paths.Layout) error {
	reader := bufio.NewReader(os.Stdin)

	for {
		client, _ := newDaemonClient(layout)
		pid, running := checkPidRunning(layout.PidFile())

		fmt.Print("\033[H\033[2J") // Clear terminal screen
		fmt.Printf("\033[1;35m┏━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━┓\033[0m\n")
		fmt.Printf("\033[1;35m┃\033[0m  🌌  \033[1;37mAURORA USERBOT\033[0m \033[36m%s\033[0m — ЦЕНТР КЕРУВАННЯ В ТЕРМІНАЛІ      \033[1;35m┃\033[0m\n", buildinfo.Version)
		fmt.Printf("\033[1;35m┗━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━┛\033[0m\n\n")

		// 1. Daemon Status
		if running {
			rss := readProcessRSS(pid)
			if rss == "" {
				rss = "активний"
			}
			fmt.Printf("  ● \033[1;32mФоновий процес:\033[0m  АКТИВНИЙ (PID %d, RAM: %s)\n", pid, rss)
		} else {
			fmt.Printf("  ● \033[1;31mФоновий процес:\033[0m  ЗУПИНЕНО (inactive)\n")
		}

		// 2. Telegram Auth Status
		var tgState = "\033[33mПеревірка...\033[0m"
		var webURL = ""
		if client != nil {
			webURL = client.baseURL + "/?token=" + client.token
			if client.isAlive() {
				if authSt, err := client.getAuth(); err == nil {
					if authSt.SignedIn {
						phone := authSt.Phone
						if phone == "" {
							phone = "авторизовано"
						}
						tgState = fmt.Sprintf("\033[1;32m✓ АВТОРИЗОВАНО\033[0m (%s)", phone)
					} else {
						tgState = fmt.Sprintf("\033[1;33m⚠ НЕ АВТОРИЗОВАНО\033[0m (стан: %s)", authSt.State)
					}
				} else {
					tgState = "\033[31mнемає відповіді від ядра\033[0m"
				}
			} else {
				tgState = "\033[2m(запустіть службу для перевірки)\033[0m"
			}
		}
		fmt.Printf("  ● \033[1;36mTelegram:\033[0m        %s\n", tgState)
		if webURL != "" {
			fmt.Printf("  ● \033[1;34mВеб-панель:\033[0m      \033[4;34m%s\033[0m\n", webURL)
		}

		fmt.Println("\n\033[2m────────────────────────────────────────────────────────────────────\033[0m")
		fmt.Println("  \033[1;33m[1]\033[0m 🔑 \033[1mВхід у Telegram\033[0m (Швидкий майстер: номер -> код -> 2FA)")
		fmt.Println("  \033[1;34m[2]\033[0m 🌐 \033[1mВідкрити Веб-панель\033[0m у браузері телефону")
		fmt.Println("  \033[1;32m[3]\033[0m ▶  \033[1mЗапустити юзербота\033[0m у фоні (aurora start)")
		fmt.Println("  \033[1;31m[4]\033[0m ⏹  \033[1mЗупинити юзербота\033[0m (aurora stop)")
		fmt.Println("  \033[1;36m[5]\033[0m 🔄 \033[1mПерезапустити юзербота\033[0m (aurora restart)")
		fmt.Println("  \033[1;35m[6]\033[0m 📋 \033[1mЖивий журнал логів\033[0m (aurora logs)")
		fmt.Println("  \033[1;36m[7]\033[0m 🧩 \033[1mКерування плагінами\033[0m (список та перемикання)")
		fmt.Println("  \033[1;32m[8]\033[0m 🧹 \033[1mОчищення пам'яті (RAM GC)\033[0m")
		fmt.Println("  \033[1;33m[9]\033[0m ⚙️  \033[1mНалаштувати власні API ключі\033[0m (my.telegram.org)")
		fmt.Println("  \033[1;31m[0]\033[0m 🚪 \033[1mВийти з Telegram акаунта\033[0m (Logout)")
		fmt.Println("  \033[2m[q]  Вийти з меню\033[0m")
		fmt.Println("\033[2m────────────────────────────────────────────────────────────────────\033[0m")
		fmt.Print("  \033[1;37mОберіть дію [1-9, 0, q]: \033[0m")

		input, err := reader.ReadString('\n')
		if err != nil {
			return nil
		}
		choice := strings.TrimSpace(input)

		switch choice {
		case "1":
			_ = cmdTerminalLogin(layout)
			pressEnterToContinue(reader)
		case "2":
			if !running {
				fmt.Println("→ Запускаю фонову службу...")
				_ = cmdStart(layout)
				time.Sleep(1 * time.Second)
			}
			openBrowserCLI(webURL)
			fmt.Printf("✓ Відкрито у браузері: %s\n", webURL)
			pressEnterToContinue(reader)
		case "3":
			if running {
				fmt.Println("✓ Юзербот уже запущений!")
			} else {
				_ = cmdStart(layout)
			}
			time.Sleep(1 * time.Second)
		case "4":
			_ = cmdStop(layout)
			time.Sleep(1 * time.Second)
		case "5":
			_ = cmdStop(layout)
			time.Sleep(500 * time.Millisecond)
			_ = cmdStart(layout)
			time.Sleep(1 * time.Second)
		case "6":
			_ = cmdLogs(layout)
		case "7":
			menuManagePlugins(client, reader)
		case "8":
			if client != nil && client.isAlive() {
				if err := client.triggerGC(); err == nil {
					fmt.Println("\033[32m✓ Очищення пам'яті (GC) успішно виконано!\033[0m")
				} else {
					fmt.Println("✖ Помилка:", err)
				}
			} else {
				fmt.Println("✖ Помилка: служба не запущена")
			}
			pressEnterToContinue(reader)
		case "9":
			_ = cmdSetup(layout)
			pressEnterToContinue(reader)
		case "0":
			fmt.Print("Ви дійсно бажаєте вийти з акаунта Telegram? [y/N]: ")
			ans, _ := reader.ReadString('\n')
			if strings.ToLower(strings.TrimSpace(ans)) == "y" {
				if client != nil && client.isAlive() {
					_ = client.logout()
				}
				_ = cmdLogout(layout)
				fmt.Println("✓ Сесію завершено!")
			}
			pressEnterToContinue(reader)
		case "q", "exit", "quit":
			fmt.Println("До зустрічі!")
			return nil
		}
	}
}

func pressEnterToContinue(r *bufio.Reader) {
	fmt.Print("\n\033[2mНатисніть Enter для повернення в меню...\033[0m")
	_, _ = r.ReadString('\n')
}

// cmdTerminalLogin is the interactive, robust CLI login flow.
func cmdTerminalLogin(layout paths.Layout) error {
	reader := bufio.NewReader(os.Stdin)

	fmt.Println("\n\033[1;36m════════════════════════════════════════════════════════════════════\033[0m")
	fmt.Println("  \033[1;37m🔑 АВТОРИЗАЦІЯ В TELEGRAM — AURORA USERBOT\033[0m")
	fmt.Println("\033[1;36m════════════════════════════════════════════════════════════════════\033[0m")

	// Ensure background daemon is running so CLI and Web share the exact same session
	client, err := newDaemonClient(layout)
	if err != nil {
		return err
	}

	if !client.isAlive() {
		fmt.Println("→ Запускаю ядро юзербота для підключення до Telegram...")
		_ = cmdStart(layout)
		// Wait up to 5s for daemon to become ready
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			time.Sleep(300 * time.Millisecond)
			if client.isAlive() {
				break
			}
		}
		if !client.isAlive() {
			return fmt.Errorf("не вдалося запустити ядро юзербота")
		}
	}

	// Check if already authorized
	if authSt, err := client.getAuth(); err == nil && authSt.SignedIn {
		fmt.Printf("✓ Цей акаунт вже авторизовано: %s\n", authSt.Phone)
		fmt.Print("Бажаєте вийти та увійти з іншого номера? [y/N]: ")
		ans, _ := reader.ReadString('\n')
		if strings.ToLower(strings.TrimSpace(ans)) != "y" {
			return nil
		}
		_ = client.logout()
		time.Sleep(500 * time.Millisecond)
	}

	// Step 1: Phone number
	fmt.Print("\n\033[1;33m[Крок 1/3]\033[0m Введіть номер телефону (\033[36m+380...\033[0m або \033[36m+48...\033[0m): ")
	rawPhone, _ := reader.ReadString('\n')
	phone := tgc.CleanPhone(strings.TrimSpace(rawPhone))
	if phone == "" {
		return fmt.Errorf("номер телефону не може бути порожнім")
	}

	fmt.Printf("→ Надсилаємо код на \033[1;37m%s\033[0m...\n", phone)
	if err := client.requestCode(phone); err != nil {
		fmt.Printf("\033[31m✖ Помилка надсилання коду: %v\033[0m\n", err)
		return err
	}
	fmt.Printf("\033[32m✓ Код підтвердження успішно надіслано в Telegram на %s!\033[0m\n", phone)

	// Step 2: Code
	fmt.Print("\n\033[1;33m[Крок 2/3]\033[0m Введіть 5-значний код із повідомлення Telegram: ")
	rawCode, _ := reader.ReadString('\n')
	code := strings.TrimSpace(rawCode)
	if code == "" {
		return fmt.Errorf("код не може бути порожнім")
	}

	fmt.Println("→ Перевірка коду...")
	signErr := client.submitCode(code)
	if signErr != nil {
		msg := signErr.Error()
		if strings.Contains(msg, "SESSION_PASSWORD_NEEDED") || strings.Contains(msg, "2FA") {
			// Step 3: 2FA Password
			fmt.Print("\n\033[1;33m[Крок 3/3]\033[0m 🔒 Акаунт захищено хмарним паролем (2FA). Введіть пароль: ")
			rawPass, _ := reader.ReadString('\n')
			pass := strings.TrimSpace(rawPass)
			fmt.Println("→ Перевірка 2FA пароля...")
			if err := client.submitPassword(pass); err != nil {
				fmt.Printf("\033[31m✖ Помилка 2FA пароля: %v\033[0m\n", err)
				return err
			}
		} else {
			fmt.Printf("\033[31m✖ Помилка входу: %v\033[0m\n", signErr)
			return signErr
		}
	}

	fmt.Println("\n\033[1;32m🎉 УСПІШНО АВТОРИЗОВАНО!\033[0m")
	fmt.Println("  Сесію збережено. Юзербот активний і готовий до роботи.")
	fmt.Println("  Веб-панель: " + client.baseURL + "/?token=" + client.token)
	return nil
}

func menuManagePlugins(client *daemonClient, reader *bufio.Reader) {
	if client == nil || !client.isAlive() {
		fmt.Println("✖ Служба не запущена. Запустіть юзербота спочатку!")
		pressEnterToContinue(reader)
		return
	}

	plugins, err := client.getPlugins()
	if err != nil {
		fmt.Printf("✖ Помилка отримання плагінів: %v\n", err)
		pressEnterToContinue(reader)
		return
	}

	fmt.Println("\n\033[1;36m🧩 СПИСОК ПЛАГІНІВ:\033[0m")
	if len(plugins) == 0 {
		fmt.Println("  (плагінів поки немає у папці ~/.local/share/aurora/plugins)")
	}
	for i, p := range plugins {
		status := "\033[31mзупинено\033[0m"
		if p.Running {
			status = "\033[32mактивний\033[0m"
		}
		fmt.Printf("  [%d] %-15s [%s] — %s\n", i+1, p.Name, status, p.Desc)
	}
	pressEnterToContinue(reader)
}
