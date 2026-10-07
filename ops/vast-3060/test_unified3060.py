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


# ---------------------------------------------------------------- quick 261001-cwi
def _od(i, base, storage, mid=None, host=None, geo="US", total=None, min_bid=None):
    o = {"id": i, "machine_id": mid if mid is not None else 1000 + i,
         "host_id": host if host is not None else 2000 + i,
         "dph_base": base, "storage_cost": storage, "geolocation": geo,
         "dph_total": total if total is not None else (base or 0) + 0.001}
    if min_bid is not None:
        o["min_bid"] = min_bid
    return o


class RealCostTest(unittest.TestCase):
    def test_ondemand_base_plus_storage(self):
        c = u.real_cost({"dph_base": 0.0533, "storage_cost": 0.2}, disk_gb=30)
        self.assertAlmostEqual(c["hourly"], 0.0533, places=6)
        self.assertAlmostEqual(c["storage_h"], 0.2 * 30 / 730, places=6)
        self.assertAlmostEqual(c["storage_h"], 0.00822, places=5)
        self.assertAlmostEqual(c["total"], 0.06152, places=5)
        self.assertEqual(c["src"], "base+storage")

    def test_bid_uses_bid_as_hourly(self):
        c = u.real_cost({"dph_base": 0.05, "storage_cost": 0.2, "min_bid": 0.03},
                        mode="bid", bid=0.0345, disk_gb=30)
        self.assertAlmostEqual(c["hourly"], 0.0345, places=6)
        self.assertAlmostEqual(c["total"], 0.0345 + 0.2 * 30 / 730, places=6)
        self.assertEqual(c["src"], "bid+storage")

    def test_fallback_dph_total(self):
        c = u.real_cost({"dph_total": 0.04})
        self.assertEqual(c["src"], "dph_total-fallback")
        self.assertAlmostEqual(c["total"], 0.04)
        self.assertEqual(c["storage_h"], 0)

    def test_fallback_missing_storage_cost(self):
        c = u.real_cost({"dph_base": 0.03, "dph_total": 0.031})
        self.assertEqual(c["src"], "dph_total-fallback")
        self.assertAlmostEqual(c["total"], 0.031)

    def test_never_raises_on_empty(self):
        c = u.real_cost({})
        self.assertGreater(c["total"], 1.0)  # inelegivel, mas sem excecao

    def test_download_amortized_into_rank_not_total(self):
        o = {"dph_base": 0.05, "storage_cost": 0.2, "inet_down_cost": 0.0267}
        c = u.real_cost(o, disk_gb=40)
        exp_dl = 0.0267 * u.DOWNLOAD_GB_PER_START / u.HOURS_PER_START
        self.assertAlmostEqual(c["download_h"], exp_dl, places=9)
        self.assertAlmostEqual(c["total"], 0.05 + 0.2 * 40 / 730, places=9)
        self.assertAlmostEqual(c["rank"], c["total"] + exp_dl, places=9)

    def test_download_missing_is_zero(self):
        c = u.real_cost({"dph_base": 0.05, "storage_cost": 0.2}, disk_gb=40)
        self.assertEqual(c["download_h"], 0.0)
        self.assertAlmostEqual(c["rank"], c["total"], places=12)

    def test_rank_prefers_cheap_download_over_cheaper_gpu(self):
        # GPU 0.002/h mais barata mas download 0.0267/GB (≈0.045/h amortizado)
        # perde para a ligeiramente mais cara com download barato.
        cheap_gpu = {"id": 1, "machine_id": 1, "host_id": 1, "geolocation": "US",
                     "dph_base": 0.050, "storage_cost": 0.2, "inet_down_cost": 0.0267}
        cheap_dl = {"id": 2, "machine_id": 2, "host_id": 2, "geolocation": "US",
                    "dph_base": 0.052, "storage_cost": 0.2, "inet_down_cost": 0.001}
        pick = u.rank_candidates([cheap_gpu, cheap_dl], [], "ondemand", disk_gb=40,
                                 price_cap=0.1, cap_steps=[1.0])
        self.assertEqual(pick["offer"]["id"], 2)

    def test_cap_applies_to_gpu_plus_storage_only(self):
        # download alto NAO tira a oferta do teto (teto = GPU+disco)
        o = {"id": 3, "machine_id": 3, "host_id": 3, "geolocation": "US",
             "dph_base": 0.05, "storage_cost": 0.2, "inet_down_cost": 1.0}
        pick = u.rank_candidates([o], [], "ondemand", disk_gb=40,
                                 price_cap=0.061, cap_steps=[1.0])
        self.assertIsNotNone(pick)

    def test_disk_default_is_40(self):
        self.assertEqual(u.DISK_GB, 40)
        c = u.real_cost({"dph_base": 0.0, "storage_cost": 0.73})
        self.assertAlmostEqual(c["storage_h"], 0.04, places=6)


class BidPriceTest(unittest.TestCase):
    def test_margin(self):
        self.assertEqual(u.bid_price_for({"min_bid": 0.03}, margin=1.15), 0.0345)

    def test_no_min_bid(self):
        self.assertIsNone(u.bid_price_for({}, margin=1.15))
        self.assertIsNone(u.bid_price_for({"min_bid": None}))

    def test_capped_to_cap_minus_storage(self):
        b = u.bid_price_for({"min_bid": 0.03}, margin=1.15, cap_total=0.04, storage_h=0.008)
        self.assertAlmostEqual(b, 0.032, places=6)
        self.assertLessEqual(b + 0.008, 0.04 + 1e-9)

    def test_capped_below_min_bid_is_none(self):
        self.assertIsNone(u.bid_price_for({"min_bid": 0.03}, margin=1.15,
                                          cap_total=0.035, storage_h=0.008))

    def test_under_cap_unchanged(self):
        self.assertEqual(u.bid_price_for({"min_bid": 0.02}, margin=1.15,
                                         cap_total=0.1, storage_h=0.008), 0.023)


class ChooseModeTest(unittest.TestCase):
    def test_ondemand_forced(self):
        self.assertEqual(u.choose_mode("ondemand", 0, 2), "ondemand")

    def test_bid_after_max_preempt(self):
        self.assertEqual(u.choose_mode("bid", 2, 2), "ondemand")
        self.assertEqual(u.choose_mode("bid", 3, 2), "ondemand")

    def test_bid_under_max(self):
        self.assertEqual(u.choose_mode("bid", 1, 2), "bid")
        self.assertEqual(u.choose_mode("bid", 0, 2), "bid")

    def test_invalid_defaults_to_bid(self):
        self.assertEqual(u.choose_mode("spot!!", 0, 2), "bid")
        self.assertEqual(u.choose_mode(None, 0, 2), "bid")


class CfgTest(unittest.TestCase):
    def test_defaults(self):
        c = u.cfg({})
        self.assertEqual((c["mode"], c["margin"], c["max_preempt"]), ("bid", 1.15, 2))

    def test_overrides(self):
        c = u.cfg({"VAST3060_MODE": "OnDemand", "VAST3060_BID_MARGIN": "1.3",
                   "VAST3060_MAX_PREEMPT": "4"})
        self.assertEqual((c["mode"], c["margin"], c["max_preempt"]), ("ondemand", 1.3, 4))

    def test_invalid_values_fall_back(self):
        c = u.cfg({"VAST3060_MODE": "x", "VAST3060_BID_MARGIN": "abc",
                   "VAST3060_MAX_PREEMPT": "-1"})
        self.assertEqual((c["mode"], c["margin"], c["max_preempt"]), ("bid", 1.15, 2))

    def test_margin_below_one_rejected(self):
        self.assertEqual(u.cfg({"VAST3060_BID_MARGIN": "0.5"})["margin"], 1.15)


class RankCandidatesTest(unittest.TestCase):
    CAP = 0.035
    STEPS = [1.0, 1.3, 1.6, 2.0]

    def rank(self, od, bid, mode="bid", **kw):
        return u.rank_candidates(od, bid, mode, disk_gb=30, margin=1.15,
                                 price_cap=self.CAP, cap_steps=self.STEPS, **kw)

    def test_real_cost_beats_dph_total(self):
        # A: dph_total menor, mas storage caro -> total real maior
        a = _od(1, 0.020, 1.0, total=0.021)   # 0.020 + 0.0411 = 0.0611
        b = _od(2, 0.030, 0.1, total=0.031)   # 0.030 + 0.0041 = 0.0341
        r = self.rank([a, b], [], mode="ondemand")
        self.assertEqual(r["offer"]["id"], 2)
        self.assertEqual(r["mode"], "ondemand")
        self.assertIsNone(r["bid"])
        self.assertEqual(r["cap_mult"], 1.0)

    def test_bid_cheaper_wins(self):
        od = _od(1, 0.0533, 0.2)                     # 0.0615
        bd = _od(2, 0.05, 0.2, min_bid=0.03)         # bid 0.0345 + 0.0082 = 0.0427
        r = self.rank([od], [bd])
        self.assertEqual(r["mode"], "bid")
        self.assertEqual(r["offer"]["id"], 2)
        self.assertAlmostEqual(r["bid"], 0.0345)
        self.assertEqual(r["cap_mult"], 1.3)
        self.assertLessEqual(r["cost"]["total"], self.CAP * 1.3)

    def test_ondemand_mode_ignores_bids(self):
        od = _od(1, 0.0533, 0.2)
        bd = _od(2, 0.05, 0.2, min_bid=0.01)
        r = self.rank([od], [bd], mode="ondemand")
        self.assertEqual(r["mode"], "ondemand")
        self.assertEqual(r["offer"]["id"], 1)

    def test_fallback_to_ondemand_without_eligible_bid(self):
        od = _od(1, 0.0533, 0.2)
        no_min = _od(2, 0.05, 0.2)  # sem min_bid -> inelegivel
        r = self.rank([od], [no_min])
        self.assertEqual(r["mode"], "ondemand")
        self.assertEqual(r["offer"]["id"], 1)
        self.assertEqual(r["cap_mult"], 2.0)

    def test_bid_list_respects_avoid_and_cn(self):
        od = _od(1, 0.0533, 0.2)
        cn = _od(2, 0.05, 0.2, min_bid=0.01, geo="Beijing, CN")
        avm = _od(3, 0.05, 0.2, min_bid=0.01, mid=777)
        avh = _od(4, 0.05, 0.2, min_bid=0.01, host=888)
        r = self.rank([od], [cn, avm, avh], machine_avoid=[777], host_avoid=[888])
        self.assertEqual(r["mode"], "ondemand")
        self.assertEqual(r["offer"]["id"], 1)

    def test_tie_prefers_ondemand(self):
        od = _od(1, 0.02, 0.0)
        bd = _od(2, 0.05, 0.0, min_bid=0.02 / 1.15)
        r = self.rank([od], [bd])
        self.assertEqual(r["mode"], "ondemand")

    def test_none_when_nothing_fits(self):
        self.assertIsNone(self.rank([_od(1, 0.5, 0.2)], [_od(2, 0.5, 0.2, min_bid=0.4)]))
        self.assertIsNone(self.rank([], []))

    def test_lowest_step_wins_even_if_higher_step_has_cheaper_bid_cap(self):
        od = _od(1, 0.030, 0.0)  # 0.030 cabe no 1.0x
        r = self.rank([od], [])
        self.assertEqual(r["cap_mult"], 1.0)


class PreemptCounterTest(unittest.TestCase):
    def test_first_of_day(self):
        st = {}
        self.assertEqual(u.bump_preempt(st, "2026-10-01"), 1)
        self.assertEqual(st["preempt_day"], "2026-10-01")
        self.assertEqual(st["preempt_count"], 1)

    def test_increments_same_day(self):
        st = {"preempt_day": "2026-10-01", "preempt_count": 1}
        self.assertEqual(u.bump_preempt(st, "2026-10-01"), 2)

    def test_resets_new_day(self):
        st = {"preempt_day": "2026-09-30", "preempt_count": 5}
        self.assertEqual(u.bump_preempt(st, "2026-10-01"), 1)

    def test_read_without_mutation(self):
        st = {"preempt_day": "2026-09-30", "preempt_count": 5}
        self.assertEqual(u.preempt_today(st, "2026-10-01"), 0)
        self.assertEqual(u.preempt_today(st, "2026-09-30"), 5)
        self.assertEqual(st["preempt_count"], 5)
        self.assertEqual(u.preempt_today({}, "2026-10-01"), 0)


class OfferQueryTest(unittest.TestCase):
    def test_kinds_and_no_mutation(self):
        self.assertEqual(u.offer_query("bid")["type"], "bid")
        self.assertEqual(u.offer_query("on-demand")["type"], "on-demand")
        self.assertEqual(u.v.OFFER_QUERY["type"], "on-demand")
        q = u.offer_query("bid")
        q["gpu_name"]["in"].append("X")
        self.assertNotIn("X", u.v.OFFER_QUERY["gpu_name"]["in"])


class _Stop(Exception):
    pass


class CreatePayloadTest(unittest.TestCase):
    """cmd_start com I/O mockado: so ate o PUT de criacao (vast_get aborta)."""

    def run_start(self, pick):
        calls, saved = [], []

        def boom(*a, **k):
            raise _Stop()

        patches = {
            "acquire_start_lock": lambda *a, **k: True,
            "load_state": lambda: {"instance_id": None, "machine_avoid": [],
                                   "host_avoid": [], "pending_id": None},
            "save_state": lambda st: saved.append(dict(st)),
            "pick_offer": lambda *a, **k: pick,
            "build_onstart": lambda: "echo hi",
            "vast_get": boom,
        }
        orig = {k: getattr(u, k) for k in patches}
        orig_http, orig_sleep, orig_notify = u.v.http_json, u.time.sleep, u.v.notify

        def fake_http(method, url, headers=None, payload=None, timeout=30):
            calls.append((method, url, payload))
            return 200, {"new_contract": 999}
        try:
            for k, f in patches.items():
                setattr(u, k, f)
            u.v.http_json, u.time.sleep, u.v.notify = fake_http, lambda s: None, lambda *a: None
            with self.assertRaises(_Stop):
                u.cmd_start({"VAST_API_KEY": "x"})
        finally:
            for k, f in orig.items():
                setattr(u, k, f)
            u.v.http_json, u.time.sleep, u.v.notify = orig_http, orig_sleep, orig_notify
        return calls, saved

    def pick(self, mode, bid):
        o = _od(7, 0.05, 0.2, min_bid=0.03)
        return {"offer": o, "mode": mode, "bid": bid, "cap_mult": 1.3,
                "cost": u.real_cost(o, mode, bid)}

    def test_bid_sends_price_and_disk40(self):
        calls, saved = self.run_start(self.pick("bid", 0.0345))
        put = [c for c in calls if c[0] == "PUT"][0]
        self.assertTrue(put[1].endswith("/asks/7/"))
        self.assertEqual(put[2]["price"], 0.0345)
        self.assertEqual(put[2]["disk"], 40)
        self.assertEqual(saved[-1]["pending_id"], 999)
        self.assertEqual(saved[-1]["pending_mode"], "bid")

    def test_ondemand_has_no_price(self):
        calls, _ = self.run_start(self.pick("ondemand", None))
        put = [c for c in calls if c[0] == "PUT"][0]
        self.assertNotIn("price", put[2])


class WatchdogWindowTest(unittest.TestCase):
    from datetime import datetime as _dt, timezone as _tz
    from zoneinfo import ZoneInfo as _zi

    def brt(self, h, m):
        return self._dt(2026, 10, 1, h, m, tzinfo=self._zi("America/Sao_Paulo"))

    def test_window_bounds(self):
        self.assertFalse(u.in_watchdog_window(self.brt(6, 59)))
        self.assertTrue(u.in_watchdog_window(self.brt(7, 0)))
        self.assertTrue(u.in_watchdog_window(self.brt(13, 30)))
        self.assertTrue(u.in_watchdog_window(self.brt(19, 59)))
        self.assertFalse(u.in_watchdog_window(self.brt(20, 0)))

    def test_converts_utc(self):
        # 10:00 UTC = 07:00 BRT (UTC-3)
        self.assertTrue(u.in_watchdog_window(self._dt(2026, 10, 1, 10, 0, tzinfo=self._tz.utc)))
        # 23:30 UTC = 20:30 BRT
        self.assertFalse(u.in_watchdog_window(self._dt(2026, 10, 1, 23, 30, tzinfo=self._tz.utc)))

    def test_reprovision_cutoff(self):
        self.assertTrue(u.reprovision_allowed(self.brt(17, 59)))
        self.assertFalse(u.reprovision_allowed(self.brt(18, 0)))
        self.assertFalse(u.reprovision_allowed(self.brt(19, 30)))


class IsTerminalTest(unittest.TestCase):
    def test_shapes(self):
        self.assertTrue(u.is_terminal({"actual_status": "exited", "intended_status": "stopped"}))
        self.assertTrue(u.is_terminal({"actual_status": "stopped"}))
        self.assertTrue(u.is_terminal({"actual_status": "running", "intended_status": "stopped"}))
        self.assertTrue(u.is_terminal({"cur_state": "stopped"}))
        self.assertFalse(u.is_terminal({"actual_status": "running", "intended_status": "running"}))
        self.assertFalse(u.is_terminal({"actual_status": "loading"}))
        self.assertFalse(u.is_terminal(None))
        self.assertFalse(u.is_terminal({}))


class WatchdogDecisionTest(unittest.TestCase):
    RUN = {"actual_status": "running", "intended_status": "running", "cur_state": "running"}
    EXITED = {"actual_status": "exited", "intended_status": "stopped", "cur_state": "stopped"}

    def d(self, **kw):
        args = dict(in_window=True, start_running=False, instance_id=123, vast_state="ok",
                    inst=self.RUN, health_ok=True, fail_streak=0, k=3)
        args.update(kw)
        return u.watchdog_decision(**args)

    def test_out_of_window_keeps_streak(self):
        self.assertEqual(self.d(in_window=False, fail_streak=2, health_ok=False),
                         ("noop_window", 2))

    def test_start_running_resets(self):
        self.assertEqual(self.d(start_running=True, fail_streak=2, vast_state="gone"),
                         ("noop_start", 0))

    def test_no_instance(self):
        self.assertEqual(self.d(instance_id=None, vast_state=None, inst=None), ("noop_none", 0))

    def test_no_instance_needs_pod_retrigger(self):
        self.assertEqual(self.d(instance_id=None, needs_pod=True), ("retrigger", 0))
        self.assertEqual(self.d(instance_id=None, needs_pod=True, minutes_since_trigger=30),
                         ("retrigger", 0))

    def test_retrigger_respects_30min(self):
        self.assertEqual(self.d(instance_id=None, needs_pod=True, minutes_since_trigger=12),
                         ("noop_none", 0))

    def test_api_error_never_counts(self):
        self.assertEqual(self.d(vast_state="error", inst=None, health_ok=False, fail_streak=2),
                         ("noop_api", 2))
        self.assertEqual(self.d(vast_state="error", inst=None, health_ok=True, fail_streak=1),
                         ("noop_api", 1))

    def test_gone_is_immediate(self):
        self.assertEqual(self.d(vast_state="gone", inst=None, health_ok=False), ("preempted", 0))
        self.assertEqual(self.d(vast_state="gone", inst=None, health_ok=True), ("preempted", 0))

    def test_terminal_and_dead_is_immediate(self):
        self.assertEqual(self.d(inst=self.EXITED, health_ok=False), ("preempted", 0))

    def test_terminal_transient_with_health_needs_k(self):
        self.assertEqual(self.d(inst=self.EXITED, health_ok=True, fail_streak=0), ("suspect", 1))
        self.assertEqual(self.d(inst=self.EXITED, health_ok=True, fail_streak=1), ("suspect", 2))
        self.assertEqual(self.d(inst=self.EXITED, health_ok=True, fail_streak=2), ("preempted", 0))

    def test_running_but_dead_needs_k(self):
        self.assertEqual(self.d(health_ok=False, fail_streak=0), ("suspect", 1))
        self.assertEqual(self.d(health_ok=False, fail_streak=1), ("suspect", 2))
        self.assertEqual(self.d(health_ok=False, fail_streak=2), ("preempted", 0))
        for st in ("loading", "offline", "unknown"):
            self.assertEqual(self.d(inst={"actual_status": st}, health_ok=False), ("suspect", 1))

    def test_ok_resets_streak(self):
        self.assertEqual(self.d(health_ok=True, fail_streak=2), ("ok", 0))


class HealthAllTest(unittest.TestCase):
    INST = {"public_ipaddr": "1.2.3.4",
            "ports": {"8000/tcp": [{"HostPort": "41000"}], "7998/tcp": [{"HostPort": "41998"}],
                      "8021/tcp": [{"HostPort": "41021"}]}}

    def run_h(self, alive_ports, inst=None):
        orig = u.health
        seen = []

        def fake(ip, port, timeout=8):
            seen.append(port)
            return port in alive_ports
        u.health = fake
        try:
            return u.pod_health(inst if inst is not None else self.INST), seen
        finally:
            u.health = orig

    def test_all_dead_is_false(self):
        ok, seen = self.run_h(set())
        self.assertFalse(ok)
        self.assertEqual(sorted(seen), [41000, 41021, 41998])

    def test_partial_is_true(self):
        self.assertTrue(self.run_h({41998})[0])

    def test_all_alive(self):
        self.assertTrue(self.run_h({41000, 41021, 41998})[0])

    def test_no_ip_or_ports_is_false(self):
        self.assertFalse(self.run_h({41000}, inst={"ports": self.INST["ports"]})[0])
        self.assertFalse(self.run_h({41000}, inst={"public_ipaddr": "1.2.3.4"})[0])


class VastGetStateTest(unittest.TestCase):
    def run_g(self, code, raw):
        orig = u.v.http
        u.v.http = lambda *a, **k: (code, raw)
        try:
            return u.vast_get_state({"VAST_API_KEY": "x"}, 5)
        finally:
            u.v.http = orig

    def test_ok(self):
        self.assertEqual(self.run_g(200, b'{"instances": {"id": 5}}'), ("ok", {"id": 5}))

    def test_gone(self):
        self.assertEqual(self.run_g(200, b'{"instances": null}'), ("gone", None))
        self.assertEqual(self.run_g(200, b'{"instances": {}}'), ("gone", None))
        self.assertEqual(self.run_g(200, b'{"instances": []}'), ("gone", None))
        self.assertEqual(self.run_g(404, b'nf'), ("gone", None))

    def test_error(self):
        self.assertEqual(self.run_g(0, b'timeout'), ("error", None))
        self.assertEqual(self.run_g(500, b'x'), ("error", None))
        self.assertEqual(self.run_g(429, b'x'), ("error", None))
        self.assertEqual(self.run_g(200, b'not json'), ("error", None))


class CmdWatchdogTest(unittest.TestCase):
    """cmd_watchdog com todo I/O mockado (sem Vast, sem systemctl real)."""
    from datetime import datetime as _dt
    from zoneinfo import ZoneInfo as _zi

    def run_wd(self, h, st, vstate=("ok", {"actual_status": "running"}), health_ok=True,
               running=False, m=0):
        state = dict(st)
        rec = {"destroy": [], "trigger": 0, "notify": [], "get": 0}

        def get_state(env, iid):
            rec["get"] += 1
            return vstate
        patches = {
            "load_state": lambda: state,
            "save_state": lambda s: state.update(s),
            "start_running": lambda: running,
            "vast_get_state": get_state,
            "pod_health": lambda inst: health_ok,
            "vast_destroy": lambda env, iid: rec["destroy"].append(iid) or 200,
            "trigger_start": lambda: rec.__setitem__("trigger", rec["trigger"] + 1) or True,
            "today_brt": lambda: "2026-10-01",
        }
        orig = {k: getattr(u, k) for k in patches}
        orig_notify = u.v.notify
        try:
            for k, f in patches.items():
                setattr(u, k, f)
            u.v.notify = lambda env, text: rec["notify"].append(text)
            now = self._dt(2026, 10, 1, h, m, tzinfo=self._zi("America/Sao_Paulo"))
            action = u.cmd_watchdog({}, now=now)
        finally:
            for k, f in orig.items():
                setattr(u, k, f)
            u.v.notify = orig_notify
        return action, state, rec

    def test_out_of_window_no_io(self):
        action, _, rec = self.run_wd(21, {"instance_id": 5})
        self.assertEqual(action, "noop_window")
        self.assertEqual(rec["get"], 0)

    def test_gone_destroys_counts_and_triggers(self):
        action, st, rec = self.run_wd(10, {"instance_id": 5}, vstate=("gone", None),
                                      health_ok=False)
        self.assertEqual(action, "preempted")
        self.assertEqual(rec["destroy"], [5])
        self.assertEqual(rec["trigger"], 1)
        self.assertIsNone(st["instance_id"])
        self.assertEqual(st["preempt_count"], 1)
        self.assertTrue(st["wd_needs_pod"])
        self.assertEqual(len(rec["notify"]), 1)

    def test_second_preempt_announces_ondemand(self):
        _, st, rec = self.run_wd(10, {"instance_id": 5, "preempt_day": "2026-10-01",
                                      "preempt_count": 1}, vstate=("gone", None))
        self.assertEqual(st["preempt_count"], 2)
        self.assertIn("modo ondemand", rec["notify"][0])

    def test_after_cutoff_no_trigger(self):
        action, st, rec = self.run_wd(18, {"instance_id": 5}, vstate=("gone", None))
        self.assertEqual(action, "preempted")
        self.assertEqual(rec["destroy"], [5])
        self.assertEqual(rec["trigger"], 0)
        self.assertFalse(st["wd_needs_pod"])
        self.assertIn("sem reprovisao", rec["notify"][0])

    def test_api_error_no_destroy(self):
        action, st, rec = self.run_wd(10, {"instance_id": 5, "wd_fail_streak": 2},
                                      vstate=("error", None))
        self.assertEqual(action, "noop_api")
        self.assertEqual(rec["destroy"], [])
        self.assertEqual(st["wd_fail_streak"], 2)

    def test_pending_start_in_flight_is_noop(self):
        action, _, rec = self.run_wd(10, {"instance_id": 5, "pending_id": 9},
                                     vstate=("gone", None))
        self.assertEqual(action, "noop_start")
        self.assertEqual(rec["destroy"], [])
        self.assertEqual(rec["get"], 0)

    def test_suspect_persists_streak(self):
        action, st, rec = self.run_wd(10, {"instance_id": 5}, health_ok=False)
        self.assertEqual(action, "suspect")
        self.assertEqual(st["wd_fail_streak"], 1)
        self.assertEqual(rec["destroy"], [])

    def test_retrigger_after_30min(self):
        st0 = {"instance_id": None, "wd_needs_pod": True,
               "wd_last_trigger": "2026-10-01T09:20:00-03:00"}
        action, st, rec = self.run_wd(10, st0)
        self.assertEqual(action, "retrigger")
        self.assertEqual(rec["trigger"], 1)
        self.assertEqual(rec["notify"], [])


    # ---- quick 261007-ou0: re-disparo apos falha final do start ----
    FAIL_ST = {"instance_id": None, "start_last_fail": "2026-10-01T09:20:00-03:00"}

    def test_start_fail_retrigger(self):
        action, st, rec = self.run_wd(10, self.FAIL_ST)
        self.assertEqual(action, "retrigger")
        self.assertEqual(rec["trigger"], 1)
        self.assertEqual(len(rec["notify"]), 1)
        self.assertIn("re-disparando", rec["notify"][0])
        self.assertEqual(st["wd_last_trigger"], "2026-10-01T10:00:00-03:00")
        self.assertEqual(st["start_retry_count"], 1)
        self.assertEqual(st["start_retry_day"], "2026-10-01")

    def test_start_fail_too_recent(self):
        st0 = {"instance_id": None, "start_last_fail": "2026-10-01T09:45:00-03:00"}
        action, _, rec = self.run_wd(10, st0)
        self.assertEqual(action, "noop_none")
        self.assertEqual(rec["trigger"], 0)
        self.assertEqual(rec["notify"], [])

    def test_start_fail_counts_from_latest_trigger(self):
        st0 = {"instance_id": None, "start_last_fail": "2026-10-01T09:00:00-03:00",
               "wd_last_trigger": "2026-10-01T09:50:00-03:00"}
        action, _, rec = self.run_wd(10, st0)
        self.assertEqual(action, "noop_none")
        self.assertEqual(rec["trigger"], 0)

    def test_start_fail_cutoff_1930(self):
        st0 = {"instance_id": None, "start_last_fail": "2026-10-01T18:00:00-03:00"}
        _, _, rec = self.run_wd(19, st0, m=30)
        self.assertEqual(rec["trigger"], 0)
        self.assertEqual(rec["notify"], [])
        _, _, rec = self.run_wd(19, st0)
        self.assertEqual(rec["trigger"], 1)

    def test_start_fail_while_running(self):
        action, _, rec = self.run_wd(10, self.FAIL_ST, running=True)
        self.assertEqual(action, "noop_start")
        self.assertEqual(rec["trigger"], 0)

    def test_start_fail_out_of_window(self):
        action, _, rec = self.run_wd(21, self.FAIL_ST)
        self.assertEqual(action, "noop_window")
        self.assertEqual(rec["trigger"], 0)

    def test_start_fail_after_stop_noop(self):
        st0 = dict(self.FAIL_ST, last_stop="2026-10-01T09:30:00-03:00")
        action, _, rec = self.run_wd(10, st0)
        self.assertEqual(action, "noop_none")
        self.assertEqual(rec["trigger"], 0)

    def test_start_fail_daily_cap(self):
        st0 = dict(self.FAIL_ST, start_retry_day="2026-10-01",
                   start_retry_count=u.MAX_START_RETRIES_DAY)
        _, st, rec = self.run_wd(10, st0)
        self.assertEqual(rec["trigger"], 0)
        self.assertEqual(len(rec["notify"]), 1)
        self.assertIn("desistindo", rec["notify"][0])
        # 2o tick: nao notifica de novo
        _, _, rec2 = self.run_wd(10, st)
        self.assertEqual(rec2["trigger"], 0)
        self.assertEqual(rec2["notify"], [])

    def test_start_retry_counter_resets_new_day(self):
        st0 = dict(self.FAIL_ST, start_retry_day="2026-09-30",
                   start_retry_count=u.MAX_START_RETRIES_DAY)
        _, st, rec = self.run_wd(10, st0)
        self.assertEqual(rec["trigger"], 1)
        self.assertEqual(st["start_retry_count"], 1)


class StartFailNeedsPodTest(unittest.TestCase):
    from datetime import datetime as _dt
    from zoneinfo import ZoneInfo as _zi

    def now(self, h=10):
        return self._dt(2026, 10, 1, h, 0, tzinfo=self._zi("America/Sao_Paulo"))

    def test_valid(self):
        self.assertTrue(u.start_fail_needs_pod(
            {"start_last_fail": "2026-10-01T09:20:00-03:00"}, self.now()))

    def test_absent_or_invalid(self):
        self.assertFalse(u.start_fail_needs_pod({}, self.now()))
        self.assertFalse(u.start_fail_needs_pod({"start_last_fail": "lixo"}, self.now()))

    def test_yesterday(self):
        self.assertFalse(u.start_fail_needs_pod(
            {"start_last_fail": "2026-09-30T19:00:00-03:00"}, self.now()))

    def test_stop_after_fail(self):
        self.assertFalse(u.start_fail_needs_pod(
            {"start_last_fail": "2026-10-01T09:20:00-03:00",
             "last_stop": "2026-10-01T09:30:00-03:00"}, self.now()))

    def test_ok_after_fail(self):
        self.assertFalse(u.start_fail_needs_pod(
            {"start_last_fail": "2026-10-01T09:20:00-03:00",
             "start_last_ok": "2026-10-01T09:40:00-03:00"}, self.now()))

    def test_ok_before_fail(self):
        self.assertTrue(u.start_fail_needs_pod(
            {"start_last_fail": "2026-10-01T09:20:00-03:00",
             "start_last_ok": "2026-10-01T08:00:00-03:00",
             "last_stop": "2026-09-30T20:00:00-03:00"}, self.now()))

    def test_retry_allowed(self):
        self.assertTrue(u.start_retry_allowed(self.now(19).replace(minute=29)))
        self.assertFalse(u.start_retry_allowed(self.now(19).replace(minute=30)))


class RecordStartFailTest(unittest.TestCase):
    def test_records(self):
        from datetime import datetime as _dt
        state = {"x": 1}
        orig = (u.load_state, u.save_state)
        try:
            u.load_state = lambda: dict(state)
            u.save_state = lambda s: state.update(s)
            now = _dt(2026, 10, 1, 9, 20, tzinfo=u.TZ)
            u.record_start_fail(now)
        finally:
            u.load_state, u.save_state = orig
        self.assertEqual(state["start_last_fail"], now.isoformat())
        self.assertEqual(state["x"], 1)


class DiskShortfallApiTest(unittest.TestCase):
    def test_small(self):
        r = u.disk_shortfall_api({"disk_space": 19.0})
        self.assertIsNotNone(r)
        self.assertIn("19", r)
        self.assertIn("40", r)

    def test_ok(self):
        self.assertIsNone(u.disk_shortfall_api({"disk_space": 40}))
        self.assertIsNone(u.disk_shortfall_api({"disk_space": 36.5}))
        self.assertIsNone(u.disk_shortfall_api({"disk_space": 36}))

    def test_no_data(self):
        self.assertIsNone(u.disk_shortfall_api({}))
        self.assertIsNone(u.disk_shortfall_api({"disk_space": None}))
        self.assertIsNone(u.disk_shortfall_api({"disk_space": "abc"}))
        self.assertIsNone(u.disk_shortfall_api(None))


class DiskProbeParseTest(unittest.TestCase):
    def test_parse(self):
        self.assertEqual(u.parse_disk_probe("==DF==\n19G 4G\n==NOSPACE==\n0\n"),
                         {"size_gb": 19, "avail_gb": 4, "nospace": False})
        self.assertEqual(u.parse_disk_probe("==DF==\n 19  4\n==NOSPACE==\n0\n"),
                         {"size_gb": 19, "avail_gb": 4, "nospace": False})

    def test_nospace(self):
        r = u.parse_disk_probe("==DF==\n40G 30G\n==NOSPACE==\n2\n")
        self.assertTrue(r["nospace"])

    def test_garbage(self):
        empty = {"size_gb": None, "avail_gb": None, "nospace": False}
        self.assertEqual(u.parse_disk_probe(""), empty)
        self.assertEqual(u.parse_disk_probe(None), empty)
        self.assertEqual(u.parse_disk_probe("rm -rf /; ==DF==\nfoo bar\n"), empty)

    def test_verdict(self):
        self.assertIsNotNone(u.disk_probe_verdict(
            {"size_gb": 19, "avail_gb": 4, "nospace": False}))
        self.assertIsNotNone(u.disk_probe_verdict(
            {"size_gb": 40, "avail_gb": 30, "nospace": True}))
        self.assertIsNone(u.disk_probe_verdict(
            {"size_gb": 40, "avail_gb": 30, "nospace": False}))
        self.assertIsNone(u.disk_probe_verdict(
            {"size_gb": None, "avail_gb": None, "nospace": False}))
        self.assertIsNone(u.disk_probe_verdict(None))


class InstSnapshotTest(unittest.TestCase):
    def test_fields(self):
        snap = u.inst_snapshot({"actual_status": "loading", "status_msg": "x" * 300,
                                "disk_space": 40, "disk_usage": 3.2,
                                "cur_state": "running", "intended_status": "running",
                                "gpu_temp": 0})
        self.assertNotIn("\n", snap)
        for k in ("actual_status", "intended_status", "cur_state", "status_msg",
                  "disk_space", "disk_usage", "gpu_temp"):
            self.assertIn(k, snap)
        self.assertIn("x" * 200, snap)
        self.assertNotIn("x" * 201, snap)

    def test_none(self):
        self.assertEqual(u.inst_snapshot(None), "sem dados da API")


class DiagApiLogTest(unittest.TestCase):
    def test_logs_api_even_when_ssh_raises(self):
        logs = []
        orig = (u.log, u.vast_get, u.ssh_pod)

        def boom(*a, **k):
            raise RuntimeError("Connection refused")
        try:
            u.log = logs.append
            u.vast_get = lambda env, iid: {"actual_status": "running", "disk_space": 19}
            u.ssh_pod = boom
            u.diag({}, {"id": 7})
        finally:
            u.log, u.vast_get, u.ssh_pod = orig
        self.assertTrue(any(m.startswith("DIAG API:") and "19" in m for m in logs), logs)


class DiskProbeTest(unittest.TestCase):
    def test_ssh_unavailable_is_none(self):
        orig = (u.log, u.ssh_pod)
        try:
            u.log = lambda m: None
            u.ssh_pod = lambda inst, cmd, timeout=90: None
            self.assertIsNone(u.disk_probe({}))

            def boom(*a, **k):
                raise RuntimeError("refused")
            u.ssh_pod = boom
            self.assertIsNone(u.disk_probe({}))
        finally:
            u.log, u.ssh_pod = orig


if __name__ == "__main__":
    unittest.main()
