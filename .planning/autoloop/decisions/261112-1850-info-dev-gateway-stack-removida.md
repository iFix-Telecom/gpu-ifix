# Aviso: o deploy automático de dev do gateway não tem mais alvo
- pedido: ação · prioridade: baixa · tempo seu: 2 min (só se quiser dev auto-deploy de volta)
- procurador: não
- phase: 17u65kmjbp9 · branch: autoloop/17u65kmjbp9-portainer-webhook-dev · tipo: aviso
- bloqueia: nada — o conserto do CI está pronto e o portão aprovou

## Contexto
O passo que ficava vermelho todo dia nos builds da branch dev (develop) já foi consertado:
agora, se o "gatilho de deploy dev" falhar, ele só avisa e deixa o build verde. Esse era o
problema que você pediu para resolver.

Mas ao investigar achei um fato que você talvez não saiba: o gatilho de deploy dev
(webhook) aponta para um ambiente de testes do gateway ("ai-gateway-dev") que **foi apagado**
e não existe mais no Portainer. Por isso ele devolvia "404". Confirmei na API do Portainer:
hoje só existem as stacks de produção do gateway; nenhuma de dev.

Ou seja: hoje, quando você sobe código novo na branch dev, **nada é recriado automaticamente**
num ambiente de teste — porque não há ambiente de teste do gateway no ar. O build gera a
imagem (ghcr .../ifix-ai-gateway:develop), mas ela não é aplicada em lugar nenhum.

## Opções
A) Deixar como está — o build dev só gera a imagem, sem recriar nada. O conserto já garante
   que isso não deixa mais o CI vermelho. Zero custo. (É o estado atual depois do meu fix.)
B) Recriar o ambiente de teste do gateway ("ai-gateway-dev" no Portainer) e atualizar o
   segredo do webhook, para voltar a ter deploy automático de dev de verdade. Custo: subir
   mais um container no servidor + 2 min seus confirmando. É decisão de infra (só você mexe
   em produção/Portainer e no segredo do GitHub).

## Recomendação
A, por enquanto. O que você pediu (parar de sujar o CI) está entregue. Recriar o ambiente de
dev do gateway é outra tarefa, de infra, e só vale a pena se você for de fato usar deploy
automático de dev. Se quiser a opção B, respondo com os passos exatos (criar a stack +
`gh secret set PORTAINER_WEBHOOK_URL_DEV_GATEWAY`).

## Resposta do Pedro
<!-- preencher -->
