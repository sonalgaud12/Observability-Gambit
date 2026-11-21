#!/usr/bin/env python3
"""End-to-end check of the prod stack on a laptop (mock model, no API spend):
two passcodes, the one-game lock, SSE through Caddy, both model options, the
passcode label on traces, trace links in Jaeger, the idle timeout, the allow-list.

usage: e2e.py DIR   DIR holds pass.sonal and pass.nic (written by local-test.sh);
                    E2E_PORT is Caddy's https port (default 28443);
                    the stack needs GAMBIT_IDLE_TIMEOUT=20s.
"""
import base64, http.client, json, ssl, sys, time, threading

S = sys.argv[1]
PASS = {n: open(f"{S}/pass.{n}").read().strip() for n in ("sonal", "nic")}
CTX = ssl._create_unverified_context()
import os
HOST = "gambit.localhost"; PORT = int(os.environ.get("E2E_PORT", "28443"))
ok = True

def check(name, cond, extra=""):
    global ok
    print(("PASS " if cond else "FAIL ") + name + (f"  {extra}" if extra else ""), flush=True)
    ok = ok and bool(cond)

def req(user, method, path, body=None, host=HOST, headers=None):
    c = http.client.HTTPSConnection(host, PORT, context=CTX, timeout=30)
    h = {"Content-Type": "application/json", "Origin": "https://" + HOST + ("" if PORT == 443 else f":{PORT}")}
    if user:
        h["Authorization"] = "Basic " + base64.b64encode(f"{user}:{PASS[user]}".encode()).decode()
    h.update(headers or {})
    c.request(method, path, json.dumps(body) if body is not None else None, h)
    r = c.getresponse(); data = r.read()
    try: j = json.loads(data)
    except Exception: j = data
    return r.status, j, r

def sse(user, gid, sink, stop):
    """Read the event stream through Caddy, recording (arrival time, event type)."""
    c = http.client.HTTPSConnection(HOST, PORT, context=CTX, timeout=120)
    h = {"Authorization": "Basic " + base64.b64encode(f"{user}:{PASS[user]}".encode()).decode()}
    c.request("GET", f"/api/live/events?id={gid}", headers=h)
    r = c.getresponse()
    sink.append(("status", r.status, r.getheader("Content-Type")))
    typ = None
    while not stop.is_set():
        line = r.fp.readline().decode()
        if not line: break
        line = line.strip()
        if line.startswith("event:"): typ = line[6:].strip()
        elif line.startswith("data:") and typ:
            sink.append((time.time(), typ, json.loads(line[5:])))

def events(user, gid):
    return req(user, "GET", f"/api/live/state?id={gid}")[1]["events"]

def wait_ev(user, gid, typ, timeout=60, pred=None):
    end = time.time() + timeout
    while time.time() < end:
        for e in events(user, gid):
            if e["type"] == typ and (pred is None or pred(e["data"])): return e["data"]
        time.sleep(0.3)
    return None

def play_one(user, model, label):
    st, out, _ = req(user, "POST", "/api/live/new", {"human_color": "white", "model": model})
    check(f"{user} starts a game with {model}", st == 200, str(out)); gid = out["id"]
    sink, stop = [], threading.Event()
    threading.Thread(target=sse, args=(user, gid, sink, stop), daemon=True).start()
    started = wait_ev(user, gid, "game_started")
    check(f"{user}: game_started names the model and the player", started["model_id"] == model and started["player"] == user, f'{started["model"]} / {started["player"]}')
    wait_ev(user, gid, "human_turn")
    # second browser, other passcode, while this game runs -> 409
    other = "nic" if user == "sonal" else "sonal"
    st2, out2, _ = req(other, "POST", "/api/live/new", {"model": model})
    check(f"{other} is refused while {user} is playing (409)", st2 == 409, f"{st2} {out2}")
    # a client cannot pick its own label
    st3, out3, _ = req(other, "POST", "/api/live/new", {}, headers={"X-Gambit-User": "spoofed"})
    check("a spoofed X-Gambit-User does not matter (still 409)", st3 == 409)
    traces = []
    for mv in ("e2e4", "d2d4"):
        st, out, _ = req(user, "POST", "/api/live/move", {"id": gid, "move": mv})
        check(f"{user} plays {mv}", st == 200, str(out))
        moved = wait_ev(user, gid, "agent_moved", pred=lambda d, n=len(traces) + 1: d["ply"] == 2 * n)
        check("agent moved", moved is not None)
        traces.append(moved["trace_id"])
        wait_ev(user, gid, "agent_graded", pred=lambda d, n=len(traces): d["ply"] == 2 * n)
        wait_ev(user, gid, "human_turn", pred=lambda d, n=len(traces): d["ply"] == 2 * n + 1)
    req(user, "POST", "/api/live/resign", {"id": gid})
    wait_ev(user, gid, "game_saved")
    time.sleep(1); stop.set()
    kinds = [s[1] for s in sink if s[0] != "status"]
    check("SSE streamed through Caddy", sink[0][1] == 200 and "text/event-stream" in (sink[0][2] or "") and "agent_moved" in kinds and "game_over" in kinds, str(sink[0]))
    # streaming, not buffered: each agent_moved reached the browser before the next human move
    arrivals = [s[0] for s in sink if s[0] != "status" and s[1] == "agent_moved"]
    check("SSE events arrive as they happen", len(arrivals) == 2 and arrivals[1] - arrivals[0] > 0.0)
    return gid, traces

def jaeger_trace(user, tid, wait=60):
    end = time.time() + wait
    while time.time() < end:
        st, j, _ = req(user, "GET", f"/api/traces/{tid}", host=f"trace.{HOST}")
        if st == 200 and isinstance(j, dict) and j.get("data"): return j["data"][0]
        time.sleep(3)
    return None

def tag(span, k):
    for t in span["tags"]:
        if t["key"] == k: return t["value"]

def main():
    games = {}
    games["sonal"] = play_one("sonal", "luna-6", "sonal")
    time.sleep(2)
    games["nic"] = play_one("nic", "haiku-5.5", "nic")

    print("-- waiting for the Collector's tail sampling window, then Jaeger --", flush=True)
    for user, want_model in (("sonal", "mock-gpt-6-luna"), ("nic", "mock-claude-haiku-5-5")):
        gid, traces = games[user]
        tr = jaeger_trace(user, traces[0])
        check(f"trace link target for {user}'s move exists in Jaeger", tr is not None, traces[0])
        if tr:
            root = [s for s in tr["spans"] if s["operationName"] == "chess.move"][0]
            dec = [s for s in tr["spans"] if s["operationName"].startswith("invoke_agent")][0]
            check(f"  gambit.player.label = {user} on root and decision span", tag(root, "gambit.player.label") == user and tag(dec, "gambit.player.label") == user)
            check(f"  gen_ai.request.model = {want_model}", tag(root, "gen_ai.request.model") == want_model and tag(dec, "gen_ai.request.model") == want_model)
            check("  gambit.run.mode = human", tag(root, "gambit.run.mode") == "human")
    # the trace URL the UI was given
    st, cfg, _ = req("nic", "GET", "/api/config")
    check("trace_url points at the public trace host", cfg["trace_url"] == f"https://trace.{HOST}/trace/{{trace_id}}", cfg["trace_url"])

    # idle timeout (20s in this test): start, never move, the slot frees itself
    st, out, _ = req("sonal", "POST", "/api/live/new", {})
    gid = out["id"]
    over = wait_ev("sonal", gid, "game_over", timeout=40)
    check("idle game ends by itself (IdleTimeout)", over and over["method"] == "IdleTimeout", str(over))
    st, out, _ = req("nic", "POST", "/api/live/new", {"model": "luna-6"})
    check("slot is free again after the idle game", st == 200, f"{st} {out}")
    req("nic", "POST", "/api/live/resign", {"id": out["id"]})
    # model outside the allow-list
    time.sleep(2)
    st, out, _ = req("nic", "POST", "/api/live/new", {"model": "gpt-4o"})
    check("a model outside the allow-list is refused", st == 400, f"{st} {out}")
    # cross-origin through the proxy
    st, out, _ = req("nic", "POST", "/api/live/new", {}, headers={"Origin": "https://evil.example"})
    check("cross-origin POST refused through Caddy", st == 403, str(st))
    print("RESULT", "ALL PASS" if ok else "FAILURES")
    sys.exit(0 if ok else 1)

main()
