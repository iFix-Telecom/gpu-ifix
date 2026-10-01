---
status: complete
quick_id: 261001-czn
---
# 261001-czn — teste de unicidade de API key sem argon2

Commit 7eac7e3. newRawKey extraído de GenerateAPIKey; teste amostra o gerador cru. auth -race sem skip: 87s (antes estourava 10 min). Comentário DefaultParams corrigido (prod ~300ms/verify). Sem mudança de comportamento em prod.
