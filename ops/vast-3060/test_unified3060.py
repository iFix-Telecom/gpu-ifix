"""Unit tests (stdlib unittest) das funcoes PURAS do unified3060.

Rodar de ops/vast-3060:  python3 -m unittest -v test_unified3060
Nenhum I/O: unified3060 / vast3060 nao tem side effect de import (load_env so
roda no __main__).
"""
import os
import sys
import unittest

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import unified3060 as u  # noqa: E402


class FilterOffersTest(unittest.TestCase):
    OFFERS = [
        {"id": 1, "machine_id": 19775, "host_id": 500, "dph_total": 0.030, "geolocation": "Poland, PL"},
        {"id": 2, "machine_id": 20570, "host_id": 500, "dph_total": 0.025, "geolocation": "Poland, PL"},
        {"id": 3, "machine_id": 30000, "host_id": 600, "dph_total": 0.040, "geolocation": "US"},
        {"id": 4, "machine_id": 38103, "host_id": 700, "dph_total": 0.010, "geolocation": "Beijing, CN"},
        {"id": 5, "machine_id": 40000, "dph_total": 0.020, "geolocation": "Germany, DE"},  # sem host_id
    ]

    def ids(self, offers):
        return [o["id"] for o in offers]

    def test_drops_machine_avoid(self):
        out = u.filter_offers(self.OFFERS, machine_avoid=[19775])
        self.assertNotIn(1, self.ids(out))

    def test_drops_host_avoid_even_with_new_machine_id(self):
        # 20570 nunca foi pro machine_avoid, mas e' o mesmo host fisico
        out = u.filter_offers(self.OFFERS, machine_avoid=[19775], host_avoid=[500])
        self.assertNotIn(1, self.ids(out))
        self.assertNotIn(2, self.ids(out))

    def test_drops_cn(self):
        self.assertNotIn(4, self.ids(u.filter_offers(self.OFFERS)))

    def test_sorts_by_price(self):
        self.assertEqual(self.ids(u.filter_offers(self.OFFERS)), [5, 2, 1, 3])

    def test_tolerates_missing_host_id(self):
        out = u.filter_offers(self.OFFERS, host_avoid=[500, 600, 700])
        self.assertEqual(self.ids(out), [5])

    def test_empty_and_none(self):
        self.assertEqual(u.filter_offers([]), [])
        self.assertEqual(u.filter_offers(None), [])


class SelectOrphansTest(unittest.TestCase):
    INSTANCES = [
        {"id": 10, "label": "stt-tts-rerank-unified"},
        {"id": 11, "label": "stt-tts-rerank-unified"},
        {"id": 12, "label": "ifix-primary-lifecycle-5"},
        {"id": 13, "label": "stt-tts-3060-auto"},
        {"id": 14, "label": "stt-tts-rerank-unified-x"},
        {"id": 15, "label": " stt-tts-rerank-unified"},
        {"id": 16, "label": None},
        {"label": "stt-tts-rerank-unified"},  # sem id
    ]

    def test_keeps_keep_id(self):
        out = [i["id"] for i in u.select_orphans(self.INSTANCES, keep_id=10)]
        self.assertEqual(out, [11])

    def test_exact_label_only(self):
        out = [i["id"] for i in u.select_orphans(self.INSTANCES)]
        self.assertEqual(out, [10, 11])

    def test_ignores_other_labels(self):
        out = [i["id"] for i in u.select_orphans(self.INSTANCES)]
        for foreign in (12, 13, 14, 15, 16):
            self.assertNotIn(foreign, out)


class OnstartTest(unittest.TestCase):
    def test_build_onstart_fits_and_has_compat_guard(self):
        # build_onstart so le arquivos irmaos; garante que o guard novo nao
        # estourou o limite de 16384 chars do onstart da API Vast.
        cmd = u.build_onstart()
        self.assertLess(len(cmd), 16384)
        with open(os.path.join(u.HERE, "onstart-unified.sh")) as f:
            body = f.read()
        self.assertIn("ld.so.conf.d", body)
        self.assertLess(body.index("cuda_compat_guard ||"), body.index("speaches-supervisor"))


class FlipTargetsTest(unittest.TestCase):
    PORTS = {"8000/tcp": 41000, "8021/tcp": 41021, "7998/tcp": 41998}

    def test_order_and_urls(self):
        self.assertEqual(u.build_flip_targets("1.2.3.4", self.PORTS), [
            ("local-stt", "http://1.2.3.4:41000"),
            ("kokoro-tts", "http://1.2.3.4:41021"),
            ("rerank-gpu", "http://1.2.3.4:41998"),
            ("embed-gpu", "http://1.2.3.4:41998"),
        ])

    def test_missing_port_raises(self):
        ports = {"8000/tcp": 41000, "7998/tcp": 41998}
        with self.assertRaises((KeyError, ValueError)):
            u.build_flip_targets("1.2.3.4", ports)

    def test_none_port_raises(self):
        ports = dict(self.PORTS, **{"8021/tcp": None})
        with self.assertRaises((KeyError, ValueError)):
            u.build_flip_targets("1.2.3.4", ports)

    def test_empty_ip_raises(self):
        with self.assertRaises(ValueError):
            u.build_flip_targets("", self.PORTS)

    def test_rows_match_legacy_envmap_ports(self):
        # cada row nova aponta pra mesma porta interna da env antiga
        legacy = {"local-stt": "UPSTREAM_STT_URL", "kokoro-tts": "UPSTREAM_TTS_KOKORO_URL",
                  "rerank-gpu": "UPSTREAM_RERANK_URL", "embed-gpu": "UPSTREAM_EMBED_GPU_URL"}
        for row, env in legacy.items():
            self.assertEqual(u.UPSTREAM_PORTS[row], u.ENVMAP[env])


class GatewayctlCmdTest(unittest.TestCase):
    def test_argv_exact(self):
        argv = u.gatewayctl_update_cmd("local-stt", "http://1.2.3.4:41000")
        self.assertEqual(argv[:-1], ["ssh", "-i", u.SSH_KEY, "-o", "BatchMode=yes",
                                     "-o", "ConnectTimeout=10", "root@10.10.10.50"])
        self.assertEqual(argv[-1],
                         "docker exec $(docker ps -q -f name=ai-gateway-prod_gateway | head -1) "
                         "/gatewayctl upstreams update --name local-stt --url http://1.2.3.4:41000")

    def test_quotes_hostile_values(self):
        # shlex.quote: nada de injecao no shell remoto
        argv = u.gatewayctl_update_cmd("x;rm -rf /", "http://1.2.3.4:1")
        self.assertIn("--name 'x;rm -rf /'", argv[-1])

    def test_invalid_url_raises_before_subprocess(self):
        for bad in ["", "ftp://h", "http://", "1.2.3.4:80", "not a url", None]:
            with self.assertRaises(ValueError, msg=repr(bad)):
                u.gatewayctl_update_cmd("local-stt", bad)

    def test_validate_url_accepts(self):
        for ok in ["http://1.2.3.4:5000", "https://h", "http://h:1/x"]:
            self.assertEqual(u.validate_url(ok), ok)

    def test_flip_upstreams_no_subprocess_on_bad_ports(self):
        # porta ausente estoura antes de qualquer subprocess.run
        called = []
        orig = u.subprocess.run
        u.subprocess.run = lambda *a, **k: called.append(a)
        try:
            with self.assertRaises((KeyError, ValueError)):
                u.flip_upstreams({}, "1.2.3.4", {"8000/tcp": 1})
        finally:
            u.subprocess.run = orig
        self.assertEqual(called, [])


if __name__ == "__main__":
    unittest.main()
