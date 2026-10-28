package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
)

const (
	defaultBluePort  = 8088
	defaultGreenPort = 8089
	secretTTL        = "1h"
	healthTimeout    = 60 * time.Second
)

var hostAliasPattern = regexp.MustCompile(`^[a-zA-Z0-9._-]+$`)

type hostPrompt struct {
	input textinput.Model
	host  string
	err   string
	done  bool
}

func newHostPrompt() hostPrompt {
	input := textinput.New()
	input.Placeholder = "alias SSH configurado em ~/.ssh/config"
	input.CharLimit = 128
	input.SetWidth(56)
	input.Focus()
	return hostPrompt{input: input}
}

func (m hostPrompt) Init() tea.Cmd {
	return textinput.Blink
}

func (m hostPrompt) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	if key, ok := message.(tea.KeyPressMsg); ok {
		switch key.String() {
		case "ctrl+c", "esc":
			return m, tea.Quit
		case "enter":
			host := strings.TrimSpace(m.input.Value())
			if !hostAliasPattern.MatchString(host) {
				m.err = "Informe um alias SSH sem espaços (não há host padrão)."
				return m, nil
			}
			m.host = host
			m.done = true
			return m, tea.Quit
		}
	}
	var command tea.Cmd
	m.input, command = m.input.Update(message)
	return m, command
}

func (m hostPrompt) View() tea.View {
	if m.done {
		return tea.NewView("Host SSH selecionado: " + m.host + "\n")
	}
	view := "Deploy blue/green do Share Secrets\n\nAlias SSH (obrigatório):\n" + m.input.View() + "\n\nEnter confirma · Esc cancela"
	if m.err != "" {
		view += "\n\n" + m.err
	}
	return tea.NewView(view)
}

type slotPrompt struct {
	options  []string
	selected int
	done     bool
}

func newSlotPrompt() slotPrompt {
	return slotPrompt{options: []string{"none", "blue", "green"}}
}

func (m slotPrompt) Init() tea.Cmd {
	return nil
}

func (m slotPrompt) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	key, ok := message.(tea.KeyPressMsg)
	if !ok {
		return m, nil
	}
	switch key.String() {
	case "ctrl+c", "esc":
		return m, tea.Quit
	case "up", "k":
		if m.selected > 0 {
			m.selected--
		}
	case "down", "j":
		if m.selected < len(m.options)-1 {
			m.selected++
		}
	case "enter":
		m.done = true
		return m, tea.Quit
	}
	return m, nil
}

func (m slotPrompt) View() tea.View {
	view := "Qual slot está recebendo tráfego no Nginx Proxy Manager?\n\n"
	for index, option := range m.options {
		marker := "  "
		if index == m.selected {
			marker = "> "
		}
		label := option
		if option == "none" {
			label = "nenhum (primeiro deploy)"
		}
		view += marker + label + "\n"
	}
	return tea.NewView(view + "\n↑/↓ seleciona · Enter confirma · Esc cancela")
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "deploy:", err)
		os.Exit(1)
	}
}

func run() error {
	if !isTerminal(os.Stdin) || !isTerminal(os.Stdout) {
		return errors.New("execute em um terminal interativo; não há host SSH padrão")
	}
	hostModel, err := tea.NewProgram(newHostPrompt()).Run()
	if err != nil {
		return fmt.Errorf("ler alias SSH: %w", err)
	}
	hostSelection, ok := hostModel.(hostPrompt)
	if !ok || !hostSelection.done {
		return errors.New("seleção do host cancelada")
	}
	slotModel, err := tea.NewProgram(newSlotPrompt()).Run()
	if err != nil {
		return fmt.Errorf("ler slot ativo: %w", err)
	}
	slotSelection, ok := slotModel.(slotPrompt)
	if !ok || !slotSelection.done {
		return errors.New("seleção do slot cancelada")
	}
	activeSlot := slotSelection.options[slotSelection.selected]
	targetSlot, targetPort := inactiveSlot(activeSlot)
	root, err := projectRoot()
	if err != nil {
		return err
	}
	if err := deploy(root, hostSelection.host, activeSlot, targetSlot, targetPort); err != nil {
		return err
	}
	return nil
}

func inactiveSlot(active string) (string, int) {
	if active == "blue" {
		return "green", defaultGreenPort
	}
	return "blue", defaultBluePort
}

func projectRoot() (string, error) {
	workingDirectory, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("obter diretório atual: %w", err)
	}
	for directory := workingDirectory; ; directory = filepath.Dir(directory) {
		if _, err := os.Stat(filepath.Join(directory, "go.mod")); err == nil {
			return directory, nil
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			return "", errors.New("não encontrei go.mod; execute a CLI dentro do checkout do projeto")
		}
	}
}

func deploy(root, host, activeSlot, targetSlot string, targetPort int) error {
	for _, command := range []string{"mise", "docker", "ssh", "curl"} {
		if _, err := exec.LookPath(command); err != nil {
			return fmt.Errorf("comando obrigatório não encontrado no PATH: %s", command)
		}
	}
	localSecurity, err := commandOutput("docker", "info", "--format", "{{json .SecurityOptions}}")
	if err != nil || !strings.Contains(localSecurity, "name=rootless") {
		return errors.New("docker local não está confirmado como rootless; selecione o contexto rootless antes do deploy")
	}
	if err := runCommand("ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=10", host, "true"); err != nil {
		return fmt.Errorf("SSH para o alias selecionado falhou: %w", err)
	}
	remoteSecurity, err := remoteOutput(host, "docker", "info", "--format", "{{json .SecurityOptions}}")
	if err != nil || !strings.Contains(remoteSecurity, "name=rootless") {
		return fmt.Errorf("docker remoto no alias %q não está confirmado como rootless", host)
	}
	tailscaleAddress, err := remoteOutput(host, "tailscale", "ip", "-4")
	if err != nil {
		return fmt.Errorf("obter IP Tailscale do host %q: %w", host, err)
	}
	tailscaleAddress = strings.TrimSpace(strings.Split(tailscaleAddress, "\n")[0])
	parsedAddress := net.ParseIP(tailscaleAddress)
	if parsedAddress == nil || parsedAddress.To4() == nil {
		return fmt.Errorf("o host %q não retornou um IPv4 Tailscale válido", host)
	}
	tailscaleAddress = parsedAddress.String()

	activeContainer := ""
	if activeSlot != "none" {
		activeContainer = "share-secrets-" + activeSlot
		if err := checkManagedContainer(host, activeContainer, activeSlot, true); err != nil {
			return fmt.Errorf("slot ativo: %w", err)
		}
	} else {
		for _, slot := range []string{"blue", "green"} {
			container := "share-secrets-" + slot
			exists, err := checkTargetContainer(host, container, slot)
			if err != nil {
				return err
			}
			if exists {
				return fmt.Errorf("o slot %s já existe; selecione o slot ativo em vez de escolher nenhum", slot)
			}
		}
	}
	targetContainer := "share-secrets-" + targetSlot
	targetExists, err := checkTargetContainer(host, targetContainer, targetSlot)
	if err != nil {
		return err
	}
	if targetExists {
		fmt.Printf("O slot inativo %q já existe e será substituído; o slot ativo será mantido.\n", targetSlot)
		confirmed, err := confirm("Substituir esse slot inativo? [s/N] ")
		if err != nil {
			return err
		}
		if !confirmed {
			return errors.New("deploy cancelado; slot existente preservado")
		}
	}
	if err := checkPort(host, targetPort, targetContainer); err != nil {
		return err
	}

	temporaryDirectory, err := os.MkdirTemp("", "share-secrets-deploy-")
	if err != nil {
		return fmt.Errorf("criar diretório temporário de build: %w", err)
	}
	defer func() {
		if err := os.RemoveAll(temporaryDirectory); err != nil {
			fmt.Fprintf(os.Stderr, "deploy: limpar diretório temporário: %v\n", err)
		}
	}()
	contextDirectory := filepath.Join(temporaryDirectory, "context")
	if err := os.Mkdir(contextDirectory, 0o700); err != nil {
		return fmt.Errorf("criar contexto Docker temporário: %w", err)
	}
	binaryPath := filepath.Join(contextDirectory, "share-secrets")
	if err := buildARM64(root, binaryPath); err != nil {
		return err
	}
	image, err := writeRuntimeDockerfile(contextDirectory, binaryPath)
	if err != nil {
		return err
	}
	if err := runCommand("docker", "build", "--platform", "linux/arm64", "--tag", image, contextDirectory); err != nil {
		return fmt.Errorf("build da imagem ARM64: %w", err)
	}
	if err := transferImage(host, image); err != nil {
		return err
	}
	if targetExists {
		if err := remoteCommand(host, "docker", "rm", "-f", targetContainer); err != nil {
			return fmt.Errorf("remover slot inativo anterior: %w", err)
		}
	}
	arguments := []string{
		"run", "-d", "--name", targetContainer,
		"--label", "com.share-secrets.managed=true",
		"--label", "com.share-secrets.slot=" + targetSlot,
		"--label", "com.share-secrets.release=" + image,
		"--restart", "unless-stopped", "--read-only",
		"--tmpfs", "/tmp:rw,noexec,nosuid,size=16m",
		"--cap-drop", "ALL", "--security-opt", "no-new-privileges:true",
		"--memory", "128m", "--cpus", "0.5", "--pids-limit", "64",
		"--publish", fmt.Sprintf("%s:%d:8080", tailscaleAddress, targetPort),
		"--env", "ADDR=:8080", "--env", "SECRET_TTL=" + secretTTL, image,
	}
	if err := remoteCommand(host, "docker", arguments...); err != nil {
		return fmt.Errorf("iniciar slot candidato %q: %w", targetSlot, err)
	}
	if err := waitForHealth(tailscaleAddress, targetPort); err != nil {
		return fmt.Errorf("slot candidato não passou no health check; tráfego continua no slot anterior (%s): %w", activeContainer, err)
	}
	printNextSteps(tailscaleAddress, targetPort, targetSlot, activeSlot)
	return nil
}

func checkManagedContainer(host, container, slot string, requireHealthy bool) error {
	labels, err := remoteOutput(host, "docker", "inspect", "--format",
		`{{index .Config.Labels "com.share-secrets.managed"}} {{index .Config.Labels "com.share-secrets.slot"}}`, container)
	if err != nil {
		return fmt.Errorf("não consegui inspecionar %s", container)
	}
	if strings.TrimSpace(labels) != "true "+slot {
		return fmt.Errorf("%s não é um slot reconhecido do Share Secrets", container)
	}
	if requireHealthy {
		health, err := remoteOutput(host, "docker", "inspect", "--format",
			`{{if .State.Health}}{{.State.Health.Status}}{{else}}no-healthcheck{{end}}`, container)
		if err != nil || strings.TrimSpace(health) != "healthy" {
			return fmt.Errorf("%s não está healthy", container)
		}
	}
	return nil
}

func checkTargetContainer(host, container, slot string) (bool, error) {
	existing, err := remoteOutput(host, "docker", "ps", "-a", "--filter", "name=^/"+container+"$", "--format", "{{.Names}}")
	if err != nil {
		return false, fmt.Errorf("não consegui verificar o container candidato %s: %w", container, err)
	}
	if strings.TrimSpace(existing) == "" {
		return false, nil
	}
	labels, err := remoteOutput(host, "docker", "inspect", "--format",
		`{{index .Config.Labels "com.share-secrets.managed"}} {{index .Config.Labels "com.share-secrets.slot"}}`, container)
	if err != nil {
		return false, fmt.Errorf("não consegui inspecionar o container candidato %s: %w", container, err)
	}
	if strings.TrimSpace(labels) != "true "+slot {
		return false, fmt.Errorf("container %s existe, mas não pertence a esta CLI; nada foi alterado", container)
	}
	return true, nil
}

func checkPort(host string, port int, allowedContainer string) error {
	script := `set -eu
port="$1"
allowed="$2"
listeners="$(docker ps --filter "publish=$port" --format '{{.Names}}')"
while IFS= read -r name; do
	if [[ -n "$name" && "$name" != "$allowed" ]]; then
		printf 'port %s is used by container %s\n' "$port" "$name" >&2
		exit 3
	fi
done <<< "$listeners"
if ss -H -ltn "sport = :$port" | grep -q . && [[ -z "$listeners" ]]; then
	printf 'port %s is already listening\n' "$port" >&2
	exit 3
fi`
	if err := runRemoteScript(host, script, strconv.Itoa(port), allowedContainer); err != nil {
		return fmt.Errorf("porta %d ocupada ou não verificável no host remoto: %w", port, err)
	}
	return nil
}

func buildARM64(root, binaryPath string) error {
	// #nosec G204 -- Executable and arguments are fixed; only the local checkout/output path varies.
	command := exec.Command("mise", "exec", "--", "go", "build", "-trimpath", "-ldflags=-s -w", "-o", binaryPath, ".")
	command.Dir = root
	command.Env = append(os.Environ(), "GOOS=linux", "GOARCH=arm64", "CGO_ENABLED=0")
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	if err := command.Run(); err != nil {
		return fmt.Errorf("compilar binário ARM64: %w", err)
	}
	return nil
}

func writeRuntimeDockerfile(contextDirectory, binaryPath string) (string, error) {
	// #nosec G304 -- binaryPath is created directly below this process's private MkdirTemp directory.
	binary, err := os.ReadFile(binaryPath)
	if err != nil {
		return "", fmt.Errorf("ler binário compilado: %w", err)
	}
	checksum := fmt.Sprintf("%x", sha256.Sum256(binary))[:16]
	image := "share-secrets:" + checksum
	dockerfile := "FROM scratch\nCOPY share-secrets /share-secrets\nUSER 65532:65532\nEXPOSE 8080\nENV ADDR=:8080\n" +
		"HEALTHCHECK --interval=10s --timeout=2s --start-period=5s --retries=6 CMD [\"/share-secrets\", \"healthcheck\"]\nENTRYPOINT [\"/share-secrets\"]\n"
	if err := os.WriteFile(filepath.Join(contextDirectory, "Dockerfile"), []byte(dockerfile), 0o600); err != nil {
		return "", fmt.Errorf("escrever Dockerfile temporário: %w", err)
	}
	return image, nil
}

func transferImage(host, image string) error {
	// #nosec G204 -- Docker is fixed; image is a checksum-derived local tag.
	save := exec.Command("docker", "save", image)
	output, err := save.StdoutPipe()
	if err != nil {
		return fmt.Errorf("preparar transferência da imagem: %w", err)
	}
	save.Stderr = os.Stderr
	if err := save.Start(); err != nil {
		return fmt.Errorf("exportar imagem: %w", err)
	}
	// #nosec G204 -- SSH destination is selected interactively and validated; remote command is fixed.
	load := exec.Command("ssh", "-o", "BatchMode=yes", host, "docker", "load")
	load.Stdin = output
	load.Stdout = os.Stdout
	load.Stderr = os.Stderr
	loadErr := load.Run()
	saveErr := save.Wait()
	if loadErr != nil {
		return fmt.Errorf("transferir/carregar imagem no host: %w", loadErr)
	}
	if saveErr != nil {
		return fmt.Errorf("exportar imagem: %w", saveErr)
	}
	return nil
}

func waitForHealth(address string, port int) error {
	client := &http.Client{Timeout: time.Second}
	deadline := time.Now().Add(healthTimeout)
	url := fmt.Sprintf("http://%s:%d/healthz", address, port)
	for time.Now().Before(deadline) {
		request, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
		if err != nil {
			return err
		}
		response, err := client.Do(request)
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusNoContent {
				return nil
			}
		}
		time.Sleep(time.Second)
	}
	return errors.New("tempo limite excedido")
}

func printNextSteps(address string, port int, candidate, active string) {
	fmt.Printf("\nSlot %s saudável em http://%s:%d/healthz\n", candidate, address, port)
	fmt.Printf("No Nginx Proxy Manager, use Scheme=http, Forward Hostname/IP=%s e Forward Port=%d.\n", address, port)
	fmt.Println("O NPM não foi alterado. Troque o backend manualmente e valide a rota real antes de encerrar qualquer slot.")
	if active == "none" {
		fmt.Println("Primeiro deploy: não havia slot anterior para rollback.")
	} else {
		fmt.Printf("Slot anterior preservado para rollback: %s.\n", active)
	}
	fmt.Println("Atenção: os segredos são mantidos em memória; trocar de slot não transfere os links ativos.")
}

func confirm(prompt string) (bool, error) {
	fmt.Print(prompt)
	answer, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false, fmt.Errorf("ler confirmação: %w", err)
	}
	answer = strings.ToLower(strings.TrimSpace(answer))
	return answer == "s" || answer == "sim" || answer == "y" || answer == "yes", nil
}

func commandOutput(name string, arguments ...string) (string, error) {
	// #nosec G204 -- Callers use fixed command names; SSH arguments are quoted at the remote-shell boundary.
	command := exec.Command(name, arguments...)
	output, err := command.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%s: %w: %s", name, err, strings.TrimSpace(string(output)))
	}
	return string(output), nil
}

func remoteOutput(host string, arguments ...string) (string, error) {
	quotedArguments := shellQuoteArgs(arguments)
	commandArguments := append([]string{"-o", "BatchMode=yes", host}, quotedArguments...)
	return commandOutput("ssh", commandArguments...)
}

func runCommand(name string, arguments ...string) error {
	// #nosec G204 -- Callers use fixed executables and validated non-shell arguments.
	command := exec.Command(name, arguments...)
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	if err := command.Run(); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}

func remoteCommand(host, name string, arguments ...string) error {
	remote := append([]string{name}, arguments...)
	quoted := shellQuoteArgs(remote)
	return runCommand("ssh", append([]string{"-o", "BatchMode=yes", host}, quoted...)...)
}

func runRemoteScript(host, script string, arguments ...string) error {
	remoteArguments := append([]string{"bash", "-s", "--"}, arguments...)
	// #nosec G204 -- Host alias is validated and remote args are shell-quoted before SSH joins them.
	command := exec.Command("ssh", append([]string{"-o", "BatchMode=yes", host}, shellQuoteArgs(remoteArguments)...)...)
	command.Stdin = strings.NewReader(script)
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	if err := command.Run(); err != nil {
		return fmt.Errorf("ssh remoto: %w", err)
	}
	return nil
}

func shellQuoteArgs(arguments []string) []string {
	quoted := make([]string, 0, len(arguments))
	for _, argument := range arguments {
		quoted = append(quoted, "'"+strings.ReplaceAll(argument, "'", "'\\''")+"'")
	}
	return quoted
}

func isTerminal(file *os.File) bool {
	info, err := file.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}
