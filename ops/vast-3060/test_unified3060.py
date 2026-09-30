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


if __name__ == "__main__":
    unittest.main()
