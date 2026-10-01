// Package redisx (apikey.go): canal Pub/Sub de revogação de API key
// (quick 260930-wpv).
//
// Payload = hex(sha256(raw key)) — o mesmo sufixo da chave de cache
// gw:apikey:<hex>. A key crua NUNCA trafega no canal. Pub/Sub é
// at-most-once: o revogador também faz DEL do cache Redis e o listener
// esvazia o L1 inteiro a cada (re)subscribe, então mensagem perdida durante
// uma queda de conexão não deixa a key válida além do TTL do L1.
package redisx

// APIKeyRevokedChannel é o canal onde o revoke publica o hex do lookup hash
// para que todas as réplicas evictem o L1 in-process.
const APIKeyRevokedChannel = "gw:apikey:revoked"
