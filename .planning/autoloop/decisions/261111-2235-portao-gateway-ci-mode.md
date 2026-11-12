# Decisão: o portão do autoloop deve rodar o CI do gateway sozinho?

- phase: 17u65kmjb1u (quick) · branch: autoloop/17u65kmjb1u-ci-gateway-pr · tipo: estratégia
- bloqueia: só o "liga/desliga" de o portão **barrar sozinho** uma quebra do gateway. O meu código já está pronto e é útil de qualquer jeito.

## O que eu já entreguei (não depende da sua resposta)
Adicionei o gatilho `pull_request` nos workflows `build-gateway.yml` e `build-dashboard.yml`.
Efeito: **quando alguém abre um PR mexendo no gateway, o GitHub roda os testes e mostra um check VERMELHO no próprio PR, antes do merge.** Era isso que faltava: hoje os testes só rodam depois que o código já entrou na develop.

## O problema que descobri (é a sua decisão)
O "portão" do autoloop desse repositório hoje está configurado **sem CI** (`CI_MODE=none`). Ou seja: o portão lê a revisão do Claude, mas **não roda nenhum teste do gateway** por conta própria. E a develop no GitHub **não exige** nenhum check para aceitar merge.

Resultado prático: com a minha mudança, **uma pessoa olhando o PR vê o vermelho**, mas a automação do autoloop ainda **não barra o merge sozinha** por causa de um teste de gateway quebrado. Para o portão barrar automático, precisa ligar o CI dele.

(Obs.: a develop aparece "vermelha" todo dia, mas é só o passo de **redeploy no Portainer** falhando — não é teste. Isso é o item B que você disse para não mexer. Não atrapalha a comparação de testes do portão.)

## Opções
A) **Ligar o CI do portão agora** apontando para o `build-gateway.yml`.
   - Prós: o portão passa a barrar sozinho qualquer quebra de teste do gateway, em todo worker futuro desse repositório. É o que todos os outros repositórios do autoloop já fazem.
   - Contras: cada rodada do portão dispara o workflow inteiro — além dos testes, ele **builda e publica uma imagem descartável** no registro (~30-40 min por rodada, para a branch e para a develop). Gasta tempo/registro à toa.

B) **Criar um workflow enxuto só-de-teste** (sem buildar imagem) e apontar o portão para ele.
   - Prós: barra sozinho igual ao A, mas **sem** o build/publicação de imagem — mais rápido e barato por rodada.
   - Contras: é um pouco mais de trabalho agora (uma tarefa nova para montar esse workflow).

C) **Deixar como está**: fica só o check vermelho visível no PR (o que entreguei), sem o portão barrar automático.
   - Prós: zero custo extra; já pega 90% do caso (quem revisa vê o vermelho).
   - Contras: se ninguém olhar, o autoloop ainda pode mergear um gateway quebrado.

## Recomendação
**B** — é o que de fato "fecha o gap do portão" (sua opção A original) sem pagar um build de imagem por rodada. Se você quiser fechar HOJE sem esperar o workflow novo, faço **A** agora e abro a tarefa do workflow enxuto como melhoria. Me diga: Q1 = A, B ou C?

## Resposta do Pedro
<!-- preencher: Q1=A|B|C -->
