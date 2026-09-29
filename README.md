# Share Secrets

Ferramenta temporária para compartilhar uma credencial quando a pessoa não consegue acessar o Bitwarden.

## Como funciona

- A senha é cifrada no navegador com AES-256-GCM; o servidor recebe somente o conteúdo cifrado e o IV.
- A chave de descriptografia fica no fragmento (`#...`) do link, que não é enviado na requisição HTTP.
- O link vence em uma hora por padrão e os dados são apagados após a primeira tentativa de abertura.
- Os dados ficam em memória: reiniciar o processo invalida todos os links existentes.
- Limite de 500 segredos ativos e 64 KiB por requisição. O TTL pode ser ajustado entre 1 minuto e 24 horas com `SECRET_TTL`.

## Executar localmente

Requer Go 1.23 ou superior.

```sh
go test ./...
go run .
```

Abra <http://localhost:8080>.

## Testar pelo Tailscale Funnel

Com o serviço em execução, habilite o Funnel para a porta 8080 conforme a configuração da sua tailnet:

```sh
tailscale funnel 8080
```

Use a URL HTTPS exibida pelo Tailscale para abrir a página. O link de compartilhamento gerado usará essa origem pública.

## Limitações operacionais

- O Funnel publica o serviço na internet. Qualquer pessoa com o link pode consumir o segredo; compartilhe-o diretamente com o destinatário por um canal confiável.
- A leitura é descartável, mas uma tentativa com link corrompido/incompatível também consome o registro, pois o servidor não consegue validar a descriptografia.
- Não há persistência, autenticação, limite por IP ou coordenação entre múltiplas instâncias. Mantenha uma instância única durante o teste.
- Evite colocar dados de longa duração ou credenciais de alto privilégio nesta POC. Após a pessoa recuperar o acesso ao cofre, prefira armazenar/rotacionar a credencial pelo Bitwarden.
