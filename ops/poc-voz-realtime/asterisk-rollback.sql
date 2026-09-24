BEGIN;
DELETE FROM ps_endpoints WHERE id IN ('poc_openai_realtime','poc_voz_ramal');
DELETE FROM ps_aors      WHERE id IN ('poc_openai_realtime','poc_voz_ramal');
DELETE FROM ps_auths     WHERE id IN ('poc_voz_ramal_auth');
DELETE FROM ps_contacts  WHERE endpoint = 'poc_voz_ramal';
COMMIT;
