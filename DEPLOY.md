# Deploy em VPS com Nginx Proxy Manager

Este deploy usa dois slots Docker rootless na VPS do app e o Nginx Proxy Manager (NPM) em outra máquina da tailnet. A CLI prepara e verifica o slot inativo; a troca de tráfego no NPM é manual. Nenhum hostname SSH ou domínio de produção fica embutido no repositório.

## Pré-requisitos

- Go 1.27.3 gerenciado pelo mise, Docker local rootless, `mise`, `docker`, `ssh` e `curl` no computador de onde o deploy será executado.
- Um alias SSH para a VPS do app, configurado em `~/.ssh/config` e autenticado por chave/agent. Exemplo genérico:

  ```sshconfig
  Host minha-vps-app
      HostName <IP-ou-hostname>
      User <usuario>
      IdentityFile ~/.ssh/<chave>
  ```

- Docker rootless e Tailscale ativo na VPS do app. A VPS do NPM precisa alcançar o IP Tailscale do app e as portas escolhidas pelas ACLs/firewall da tailnet.
- Um domínio apontado para o NPM e um certificado TLS disponível nele, se o acesso for público.

O alias é solicitado pela CLI em cada execução. O endereço do host nunca é salvo no código ou em configuração do projeto.

Opcionalmente, carregue `METRICS_TOKEN` de um gerenciador de segredos no ambiente do operador antes de `make deploy` para habilitar métricas agregadas; use um valor com pelo menos 32 caracteres e sem espaços. A CLI envia esse valor pela entrada padrão do SSH em um arquivo temporário protegido e o remove após criar o contêiner, sem colocá-lo nos argumentos/logs. O Docker mantém o valor na configuração do contêiner, portanto usuários com acesso administrativo ao daemon remoto podem inspecioná-lo. Configure também `TRUSTED_PROXIES` com o IP/CIDR exato do NPM para que o rate limit por cliente use o cabeçalho encaminhado; `ACTIVE_SECRET_LIMIT` (1–700), `CREATE_RATE_PER_MINUTE` e `CREATE_RATE_BURST` podem ser fornecidos no ambiente do operador.

## Primeiro deploy

Execute no checkout do projeto:

```sh
make deploy
```

1. Digite o alias SSH quando solicitado.
2. Na lista de slot ativo, escolha **nenhum (primeiro deploy)**.
3. A CLI confirma o acesso SSH, verifica os daemons Docker, obtém o IP Tailscale, confere a porta, compila o binário ARM64 localmente, transfere só a imagem e inicia o slot `blue` na porta `8088`.
4. Aguarde o health check. A CLI imprime o IP Tailscale e a porta. Ela não cria ou modifica hosts no NPM.

### Configurar o Nginx Proxy Manager

Na interface do NPM:

1. Abra **Hosts → Proxy Hosts → Add Proxy Host** (ou edite o host existente).
2. Em **Details**, informe o domínio e configure:
   - **Scheme:** `http`
   - **Forward Hostname / IP:** IP Tailscale impresso pela CLI
   - **Forward Port:** `8088` no primeiro deploy
3. Salve. Em **SSL**, selecione/solicite o certificado correspondente e habilite **Force SSL** se desejado.
4. Teste `/healthz` pela rota do domínio (resposta HTTP `204`) e abra a página. Confirme também que o container NPM consegue alcançar o peer Tailscale; conectividade do host da VPS, sozinha, não garante conectividade do container.

Não use Tailscale Funnel para esse fluxo: o NPM encaminha pela tailnet diretamente ao IP e porta do slot.

## Deploys seguintes

Execute novamente `make deploy`, escolha o alias SSH e informe o slot que atualmente recebe tráfego no NPM:

- `blue` ativo → a CLI prepara `green` na porta `8089`.
- `green` ativo → a CLI prepara `blue` na porta `8088`.

Se já existir um container reconhecido no slot inativo, a CLI pede confirmação antes de substituí-lo. O slot ativo não é parado nem sobrescrito. Após o candidato passar no health check:

1. Edite o Proxy Host do NPM para apontar para o IP Tailscale impresso e a nova porta (`8088` ou `8089`).
2. Salve e verifique o caminho real pelo domínio, incluindo `/healthz`.
3. Mantenha o slot anterior em execução durante a janela de rollback. Se a verificação falhar, volte o **Forward Port** do NPM para a porta anterior. Não remova o slot antigo implicitamente.
4. Na próxima execução, informe o slot que estiver recebendo tráfego. Se a porta do slot estiver ocupada por outro processo/container, a CLI interrompe o deploy sem substituir esse recurso.

## Dados em memória e rollback

Cada slot tem seu próprio armazenamento volátil. A troca de rota não migra segredos entre slots: links criados no slot anterior deixam de funcionar enquanto o NPM aponta para o novo. Um rollback também não recupera links criados no novo slot. Reiniciar um slot apaga os segredos que ele mantinha. Evite usar o app para segredos importantes ou de longa duração.

### SQLite opcional — integração em validação

O alvo segue uma VPS, com SQLite/modernc e PostgreSQL open-source como opção
futura: consulte [`docs/roadmap.md`](docs/roadmap.md). O backend SQLite e sua
integração na CLI estão em validação: **a campanha atual falhou sob carga** e
não confirma prontidão para produção. Não execute estes passos sem finalizar a
validação e sem autorização específica para a alteração na VPS.

O operador precisa preparar previamente um diretório absoluto no filesystem
local da VPS, com permissões para UID 65532 do contêiner rootless e exclusão de
backups/snapshots. A pasta inteira contém DB, WAL e SHM; todos são sensíveis.
O arquivo `secrets.db` deve ser previamente criado/migrado uma única vez com o
Goose pinado, usando `-env none` e a migration do mesmo release. O servidor
recusa schema incompatível; a CLI **não migra** nem altera permissões.

Para um deploy autorizado, configure `STORAGE_BACKEND=sqlite`,
`SQLITE_DATA_DIR` (caminho absoluto da pasta na VPS) e
`SQLITE_NO_PAYLOAD_BACKUP_ACK=yes` no ambiente do operador antes de
`make deploy`. A CLI verifica a presença do banco e monta a mesma pasta no
slot candidato. Se o slot ativo já usa SQLite, a CLI recusa um diretório
diferente ou troca silenciosa para memória. A migração inicial de um slot
volátil exige também `SQLITE_VOLATILE_CUTOVER_ACK=yes`: links do slot antigo
não serão migrados e uma volta ao release volátil não recupera links novos.
Versões de schema devem permanecer compatíveis entre os dois slots durante o
rollback; não suponha que voltar a imagem reverte uma migration.

A inspeção da pasta/arquivo e a confirmação não provam que o backup do host ou
do provedor está excluído, que os dados são íntegros ou que dois deploys
simultâneos estão coordenados. Serialize os deploys operacionalmente. O health
check cobre startup, não prova fluxo de leitura/consumo, exclusão física nem
capacidade de carga. A política de rollback e migração deve ser avaliada antes
de cada release.

Antes de persistir payloads, acordar a política de exclusão e backups em
[`docs/storage-and-erasure.md`](docs/storage-and-erasure.md). Snapshots da VPS,
backups do volume, swap e dumps são cópias distintas; apagar uma linha não as
limpa. Não criar promessa de apagamento integral nem restaurar backups em
produção como teste. Limites de CPU/RAM e quantidade de links não são evidência
de capacidade: veja [`docs/performance.md`](docs/performance.md).

## Diagnóstico

- **Alias SSH não conecta:** teste `ssh <alias>` no computador operador e valide usuário/chave em `~/.ssh/config`.
- **Docker não é rootless:** selecione o contexto local rootless e confira que o usuário remoto tem acesso ao daemon rootless; a CLI falha fechada se não confirmar ambos.
- **NPM retorna 502:** teste do container NPM a conectividade ao IP Tailscale/porta do slot; confira ACLs, firewall e health check.
- **Health check falhou:** o slot ativo e a configuração do NPM não foram alterados. Leia o erro exibido pela CLI e inspecione somente os logs do container candidato.

O deploy não altera DNS, certificados, ACLs, firewall ou configuração do NPM e não faz limpeza de imagens/containers antigos.
