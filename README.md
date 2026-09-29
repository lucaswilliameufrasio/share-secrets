# Share Secrets

Ferramenta temporária para compartilhar uma credencial quando a pessoa não consegue acessar o Bitwarden.

## Como funciona

- O segredo é cifrado no navegador com AES-256-GCM; o servidor recebe ciphertext, IV e o hash de uma prova aleatória, nunca a chave nem o segredo em claro.
- A prova aleatória está dentro do próprio payload autenticado. Após descriptografar, o navegador a envia no ACK; o servidor compara seu SHA-256 antes de consumir o segredo.
- A chave de descriptografia fica no fragmento (`#...`) do link, que não é enviado na requisição HTTP.
- O link vence em uma hora por padrão. Abrir cria uma reserva temporária de 30 segundos; o segredo só é apagado após o navegador confirmar que conseguiu descriptografá-lo. Falhas liberam a reserva, que também expira sozinha.
- Os dados ficam em memória: reiniciar o processo invalida todos os links existentes.
- Limite de 500 segredos ativos e 64 KiB por requisição. O TTL pode ser ajustado entre 1 minuto e 24 horas com `SECRET_TTL`.

## Executar localmente

Requer Go 1.27.1 (a versão local é fixada em `.mise.toml`) e Node.js/npm somente para os testes E2E.

```sh
mise install
go test -count=1 ./...
go run .
```

Abra <http://localhost:8080>.

## Testes e verificações

O workflow do GitHub Actions executa automaticamente formato, vet, testes com race detector, build, lint, verificações de segurança, E2E e auditoria npm em push e pull request.

Os comandos mais usados também estão disponíveis no `Makefile` (`make help` lista os alvos):

```sh
make setup       # Go via mise, ferramentas Go pinadas + npm + Chromium
make test        # testes Go sem cache
make test-race   # detector de data races
make check build # formatação, vet e build
make security    # govulncheck + gosec
make bench       # benchmarks com alocações
make e2e         # fluxo real com Playwright
make lint        # golangci-lint, sem instalação global
# make tools-update # atualizar ferramentas; revisar go.mod/go.sum

# Equivalentes Go diretos
go test -count=1 ./...
go test -race -count=1 ./...
```

O E2E sobe a aplicação localmente e verifica criação, descriptografia no navegador e consumo único — inclusive que uma chave alterada não consome o segredo e permite tentar o link original. Os benchmarks são diagnósticos, não um limite de performance fixo.

## Expor temporariamente com Tailscale Funnel

Use o Funnel somente para um compartilhamento temporário. Instale e autentique o Tailscale na máquina host, confirme que a tailnet permite Funnel e inicie o app:

```sh
mise exec -- go run .
```

Em outro terminal, exponha a porta local 8080:

```sh
tailscale funnel 8080
```

Use a URL HTTPS mostrada pelo Tailscale para abrir a página e criar o link; a origem pública será usada no link gerado. Verifique o link em uma janela privada/dispositivo externo antes de enviar. Ao terminar, interrompa o Funnel (`Ctrl-C` no comando interativo ou `tailscale funnel --https=443 off`, conforme a versão do cliente) e pare o servidor. Confira `tailscale funnel status` para garantir que não há endpoint publicado.

### Alternativas

- **Cloudflare Tunnel (`cloudflared tunnel`)**: publica um serviço local por meio de um túnel Cloudflare; requer conta/configuração DNS para hostname estável. Restrinja o hostname com Cloudflare Access quando o destinatário puder autenticar.
- **Reverse proxy com HTTPS** (por exemplo, Caddy ou Nginx atrás de uma VM/VPS): adequado quando já existe domínio e infraestrutura. Configure TLS e restrinja acesso na borda, se possível.

Essas opções também podem tornar o app acessível pela internet. HTTPS protege o transporte, mas não impede que outra pessoa com o link consuma o segredo. Envie o link por canal confiável e evite dados de longa duração ou credenciais de alto privilégio.

## Limitações operacionais

- O Funnel publica o serviço na internet. Qualquer pessoa com o link pode consumir o segredo; compartilhe-o diretamente com o destinatário por um canal confiável.
- O servidor valida a prova que só aparece no plaintext autenticado por AES-GCM; isso verifica a descriptografia, mas não prova que uma pessoa leu a tela. Uma reserva ativa impede aberturas concorrentes até ser confirmada, liberada ou expirar.
- Não há persistência, autenticação, limite por IP ou coordenação entre múltiplas instâncias. Mantenha uma instância única durante o teste.
- Evite colocar dados de longa duração ou credenciais de alto privilégio nesta POC. Após a pessoa recuperar o acesso ao cofre, prefira armazenar/rotacionar a credencial pelo Bitwarden.
- O processo tem timeouts HTTP e limite de cabeçalhos, mas não possui autenticação, rate limit por IP ou proteção contra abuso volumétrico. Exponha-o apenas pelo tempo necessário.
