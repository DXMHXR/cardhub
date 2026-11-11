import { View, div, svg, image } from "gpui-kit";
import { v_flex, h_flex, InputState, Input, TextView, Popup } from "gpui-base";
import { theme, settings, home, appHome, hc, exec, terminals, readTextFile, writeTextFile, watchFolder } from "herdr";

// CardHub's GUI for Herdr's side panel. It draws nothing of its own data: `cardhub -gui` (which this
// mod keeps running in the hidden `cardhub` terminal) publishes what the panel shows as
// gui/state.json and takes every click here as a JSON file in gui/commands/. Outside Herdr CardHub
// stays the terminal app; here this panel is its only view.
const CARDHUB = "cardhub"; // on PATH; needs v0.8.0 or later (-gui)
const DIR = `${home()}/Library/Application Support/cardhub/gui`;
const ICON = (name) => `mods/cardhub/${name}.svg`;
// Pictures in a detail: cardhub -gui downloads them to gui/images/, and image() only draws files
// inside Herdr's own folder, so the panel copies each one here before drawing it.
const CACHE = "mods/cardhub/.pictures"; // hidden: Herdr reloads a mod on any other file saved in its folder

// A description or comment cut at its pictures (markdown ![alt](url) and HTML <img src>), so each
// picture is drawn where it stands: [{text} | {url, alt}].
const PICTURE = /!\[([^\]]*)\]\(\s*<?([^)\s>]+)>?(?:\s+"[^"]*")?\s*\)|<img\b([^>]*?)\bsrc\s*=\s*["']([^"']+)["']([^>]*)>/gi;
// The pictures cardhub downloads: ones attached on GitHub. Others stay links.
function isAttachment(url) {
  return /^https:\/\/((private-)?user-images\.githubusercontent\.com\/|github\.com\/(user-attachments\/assets\/|.*\/assets\/))/.test(url);
}

function splitPictures(text) {
  const parts = [];
  let last = 0;
  for (const m of (text ?? "").matchAll(PICTURE)) {
    const url = (m[2] ?? m[4]).replace(/&amp;/g, "&");
    if (!/^https?:\/\//.test(url)) continue;
    if (m.index > last) parts.push({ text: text.slice(last, m.index) });
    const alt = m[1] ?? (/\balt\s*=\s*["']([^"']*)["']/i.exec(`${m[3]} ${m[5]}`)?.[1] ?? "");
    parts.push({ url, alt });
    last = m.index + m[0].length;
  }
  if (last < (text ?? "").length) parts.push({ text: text.slice(last) });
  return parts;
}

function tint(color, alpha) {
  if (typeof color !== "string" || !/^#[0-9a-f]{6}$/i.test(color)) return color;
  return color + Math.round(alpha * 255).toString(16).padStart(2, "0");
}

const GH = { green: "#3fb950", purple: "#a371f7", red: "#f85149", gray: "#8b949e", amber: "#d29922", blue: "#58a6ff" };

function ago(msTime) {
  if (!msTime) return "";
  const s = Math.max(0, Math.round((Date.now() - msTime) / 1000));
  if (s < 60) return `${s}s ago`;
  const m = Math.round(s / 60);
  if (m < 60) return `${m}m ago`;
  const h = Math.round(m / 60);
  if (h < 48) return `${h}h ago`;
  return `${Math.round(h / 24)}d ago`;
}

function when(msTime) {
  if (!msTime) return "";
  const d = new Date(msTime);
  const mon = ["Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"][d.getMonth()];
  return `${d.getDate()} ${mon} ${d.getFullYear()} ${String(d.getHours()).padStart(2, "0")}:${String(d.getMinutes()).padStart(2, "0")}`;
}

const plural = (n, word) => `${n} ${n === 1 ? word : `${word}s`}`;
const shortRef = (ref) => ref.includes("/") ? ref.slice(ref.indexOf("/") + 1) : ref;

function prColor(p) {
  if (p.state === "MERGED") return GH.purple;
  if (p.state === "CLOSED") return GH.red;
  if (p.draft) return GH.gray;
  return GH.green;
}

function stateColor(state) {
  return { OPEN: GH.green, MERGED: GH.purple, CLOSED: GH.purple, DRAFT: GH.gray }[state] ?? GH.gray;
}

const REVIEW = { APPROVED: ["approved", GH.green], CHANGES_REQUESTED: ["changes requested", GH.red], REVIEW_REQUIRED: ["review required", GH.amber] };
const CHECKS = { SUCCESS: ["✓ passing", GH.green], FAILURE: ["✗ failing", GH.red], ERROR: ["✗ error", GH.red], PENDING: ["◷ running", GH.amber], EXPECTED: ["◷ expected", GH.amber] };


// --- search completion ---------------------------------------------------------------------------
// The board's search box completes GitHub's project filter the way github.com's filter bar does:
// qualifiers first ("lab" → label:), then the values that qualifier takes (label:b → label:bug),
// a comma for another value of the same qualifier, "-" in front to negate. Values come from the
// project (its single-select fields and their options) and from the cards seen on it so far.
const SUGGEST_MAX = 8;

function qualifiers(sg) {
  const people = ["@me", ...(sg?.assignees ?? [])];
  const plain = (xs) => xs.map((name) => ({ name }));
  const fields = (sg?.fields ?? []).map((f) => ({ key: f.key, hint: `${f.name} field`, values: f.options ?? [] }));
  const nullable = ["assignee", "label", "milestone", ...fields.map((f) => f.key)];
  const list = [
    { key: "assignee", hint: "Assigned to someone", values: plain(people) },
    { key: "label", hint: "Has a label", values: plain(sg?.labels ?? []) },
    ...fields,
    { key: "is", hint: "State or kind of item", values: plain(["open", "closed", "merged", "draft", "issue", "pr"]) },
    { key: "no", hint: "Missing a field", values: plain(nullable) },
    { key: "has", hint: "Has a field set", values: plain(nullable) },
    { key: "repo", hint: "In a repository", values: plain(sg?.repos ?? []) },
    { key: "milestone", hint: "In a milestone", values: plain(sg?.milestones ?? []) },
    { key: "author", hint: "Opened by someone", values: plain(people) },
    { key: "reason", hint: "Why it was closed", values: plain(["completed", "not planned", "reopened"]) },
    { key: "updated", hint: "Last updated", values: plain(["@today", ">@today-7d", ">@today-30d", "<@today-30d"]) },
  ];
  const seen = new Set();
  return list.filter((q) => !seen.has(q.key) && seen.add(q.key));
}

// The token being typed: the text after the last space outside double quotes.
function lastToken(text) {
  let quoted = false, start = 0;
  for (let i = 0; i < text.length; i++) {
    if (text[i] === '"') quoted = !quoted;
    else if (text[i] === " " && !quoted) start = i + 1;
  }
  return { start, token: text.slice(start) };
}

const quoteValue = (v) => /[\s,"]/.test(v) ? `"${v.replace(/"/g, "")}"` : v;
const unquote = (v) => v.replace(/^"/, "").replace(/"$/, "");

// What to offer for text: [{label, hint, color, apply: the text after picking it, more: keep open}].
function suggestions(text, sg) {
  const { start, token } = lastToken(text);
  const before = text.slice(0, start);
  const neg = token.startsWith("-") ? "-" : "";
  const body = token.slice(neg.length);
  const qs = qualifiers(sg);
  const colon = body.indexOf(":");
  if (colon < 0) {
    const q = body.toLowerCase();
    const out = qs.filter((x) => x.key.startsWith(q) && x.key !== q).map((x) => ({
      label: `${neg}${x.key}:`, hint: x.hint, apply: `${before}${neg}${x.key}:`, more: true,
    }));
    if (!neg && body && "or".startsWith(q) && before.trim()) out.push({ label: "OR", hint: "Match either side", apply: `${before}OR `, more: true });
    return out;
  }
  const key = body.slice(0, colon).toLowerCase();
  const qual = qs.find((x) => x.key === key);
  if (!qual) return [];
  const rest = body.slice(colon + 1);
  let quoted = false, cut = 0;
  for (let i = 0; i < rest.length; i++) {
    if (rest[i] === '"') quoted = !quoted;
    else if (rest[i] === "," && !quoted) cut = i + 1;
  }
  const done = rest.slice(0, cut);
  const typed = unquote(rest.slice(cut)).toLowerCase();
  const picked = new Set(done.split(",").map((v) => unquote(v).toLowerCase()).filter(Boolean));
  const head = `${before}${neg}${body.slice(0, colon + 1)}${done}`;
  const scored = qual.values
    .filter((v) => !picked.has(v.name.toLowerCase()))
    .map((v) => ({ v, at: v.name.toLowerCase().indexOf(typed) }))
    .filter((x) => x.at >= 0 && !(x.v.name.toLowerCase() === typed && typed))
    .sort((a, b) => (a.at === 0) === (b.at === 0) ? 0 : a.at === 0 ? -1 : 1);
  return scored.map(({ v }) => ({ label: v.name, color: v.color, hint: `${neg}${key}:${quoteValue(v.name)}`, apply: `${head}${quoteValue(v.name)} ` }));
}

export default class CardHub extends View {
  init(_props, cx) {
    this.state = null;
    this.text = null;
    this.alive = null;
    this.seq = 0;
    this.page = null;     // "projects" or "repos": a picker over the screen
    this.confirm = null;  // {number, method, label}: the merge about to happen
    this.method = {};     // merge method picked per pull request
    this.filter = "";
    this.search = InputState.new({ placeholder: "Search, or filters like label:bug assignee:@me" });
    this.sugOpen = false; // the completion list under the search box
    this.sugIdx = -1;     // its highlighted row, -1 for none
    this.search.on("submit", (_e, cx) => {
      if (this.sugOpen && this.sugIdx >= 0 && this.acceptSuggestion(cx)) return;
      this.sugOpen = false;
      this.send(cx, { cmd: "search", text: this.search.value() });
      cx.notify();
    });
    this.search.on("change", (_e, cx) => {
      if (this.applying) return;
      this.sugOpen = this.searchFocused; this.sugIdx = -1; cx.notify();
    });
    // ↑ ↓ Tab Esc steer the completion list. As bindings in the search box's own context they
    // beat the editor's and the window's (on_key_down hears them only after those).
    try {
      cx.bind_keys(["up", "down", "ctrl-p", "ctrl-n", "tab", "escape"].map((k) =>
        ({ keystroke: k, action: `cardhub-sug-${k}`, context: "CardHubSearch > Input" })));
    } catch (e) { console.log(`cardhub: completion keys not bound: ${e}`); }
    this.search.on("focus", (_e, cx) => { this.searchFocused = true; this.sugOpen = true; this.sugIdx = -1; cx.notify(); });
    this.search.on("blur", (_e, cx) => {
      this.searchFocused = false;
      // later, so a click on a suggestion lands before the list goes
      cx.spawn(async (cx) => { await cx.sleep(200); if (!this.searchFocused) { this.sugOpen = false; cx.notify(); } });
    });
    this.pick = InputState.new({ placeholder: "Type to filter…" });
    this.pick.on("change", (_e, cx) => { this.filter = this.pick.value(); cx.notify(); });
    this.cached = {}; // picture file -> "copying" | "ready" | "failed"
    cx.spawn(async () => {
      try { await exec("/bin/mkdir", ["-p", `${appHome()}/${CACHE}`]); } catch { /* drawn as links then */ }
      await this.prunePictures();
    });
    cx.timer.every(6 * 3600 * 1000, (cx) => cx.spawn(async () => this.prunePictures()));
    this.load(cx);
    cx.spawn(async (cx) => {
      for (;;) {
        try { await watchFolder(DIR); } catch { await cx.sleep(2000); }
        await this.reload(cx);
      }
    });
    cx.timer.every(1500, (cx) => this.reload(cx)); // in case a folder event is missed
    this.ensure(cx);
    cx.timer.every(30000, (cx) => this.ensure(cx));
    cx.timer.every(10000, (cx) => cx.notify()); // "12s ago"
  }

  load(cx) { cx.spawn(async (cx) => this.reload(cx)); }

  async reload(cx) {
    let text = null;
    try { text = await readTextFile(`${DIR}/state.json`); } catch { /* not there yet */ }
    if (!text || text === this.text) return;
    try {
      const s = JSON.parse(text);
      const first = !this.state;
      this.state = s;
      this.text = text;
      if (first || s.search !== this.lastSearch) {
        this.lastSearch = s.search;
        if (this.search.value() !== s.search) this.search.set_value(s.search ?? "");
      }
      cx.notify();
    } catch { return; /* caught mid-write: the next change reads it */ }
    if (await this.copyPictures(this.state.detail?.images)) cx.notify();
  }

  // `cardhub -gui` runs in the hidden `cardhub` terminal: start it when it's missing, and replace a
  // terminal-only cardhub from before the GUI.
  ensure(cx) { cx.spawn(async (cx) => this.ensureNow(cx)); }

  async ensureNow(cx) {
    const live = terminals().filter((x) => x.name === "cardhub" && x.state === "live");
    const gui = live.find((x) => (x.argv ?? []).includes("-gui"));
    const pid = this.state?.pid;
    if (gui) {
      if (pid) {
        let ok = false;
        try { ok = (await exec("kill", ["-0", String(pid)], { timeout: 3000 })).code === 0; } catch { /* no */ }
        if (ok !== this.alive) { this.alive = ok; cx.notify(); }
      }
      return;
    }
    this.alive = false;
    cx.notify();
    try {
      for (const x of live) await hc(["kill", x.address]);
      await hc(["run", "--bg", "--json", "--name", "cardhub", "--no-parent", "--cwd", home(), "--", CARDHUB, "-gui"]);
      this.alive = true;
    } catch (e) { console.log(`cardhub: couldn't start: ${e}`); }
    cx.notify();
  }

  send(cx, command) {
    const n = String(++this.seq).padStart(6, "0");
    const at = Date.now();
    cx.spawn(async () => {
      try { await writeTextFile(`${DIR}/commands/${at}-${n}.json`, JSON.stringify({ ...command, at }), null); }
      catch (e) { console.log(`cardhub: command not sent: ${e}`); }
    });
  }

  open(url) { if (url) exec("open", [url]).catch(() => {}); }

  // cardhub -gui decides which pictures to keep (it prunes gui/images by age and size): drop the
  // copies whose original is gone, so CACHE never outgrows it.
  async prunePictures() {
    const script = 'for f in "$1"/*; do [ -f "$f" ] && [ ! -e "$2/${f##*/}" ] && rm -f "$f" && echo "${f##*/}"; done; true';
    try {
      const r = await exec("/bin/sh", ["-c", script, "sh", `${appHome()}/${CACHE}`, `${DIR}/images`], { timeout: 20000 });
      for (const f of (r.stdout ?? "").split("\n").filter(Boolean)) delete this.cached[f];
    } catch (e) { console.log(`cardhub: pictures not pruned: ${e}`); }
  }

  // Copies the detail's new pictures into CACHE; true when any was copied (or failed to be).
  async copyPictures(images) {
    const todo = Object.values(images ?? {}).map((img) => img.file).filter((f) => f && !this.cached[f]);
    for (const f of todo) this.cached[f] = "copying";
    await Promise.all(todo.map(async (f) => {
      let ok = false;
      try { ok = (await exec("/bin/cp", [`${DIR}/images/${f}`, `${appHome()}/${CACHE}/${f}`], { timeout: 10000 })).code === 0; } catch (e) { console.log(`cardhub: picture not copied: ${e}`); }
      this.cached[f] = ok ? "ready" : "failed";
    }));
    return todo.length > 0;
  }

  // Markdown with its pictures drawn in place, each at most width wide (and 560 tall), opening
  // on GitHub when clicked. One still loading shows as such; one that can't be shown, as a link.
  rich({ t }, id, text, images, width) {
    const parts = splitPictures(text?.trim() ? text : "*(empty)*");
    return v_flex().gap(8).children(parts.map((p, i) => {
      if (p.text != null) {
        return p.text.trim() ? TextView.markdown(`${id}-${i}`, p.text).selectable(true).on_link_click((url) => this.open(url)) : div();
      }
      const img = images?.[p.url];
      const label = p.alt || p.url.replace(/^https?:\/\//, "").slice(0, 60);
      const copied = img?.file ? this.cached[img.file] : null;
      if (img?.file && copied === "ready") {
        const scale = Math.min(1, (width || 320) / img.w, 560 / img.h);
        const w = Math.max(1, Math.round(img.w * scale)), h = Math.max(1, Math.round(img.h * scale));
        return div().id(`${id}-img-${i}`).flex_none().w(w).h(h).rounded(6).overflow_hidden().cursor_pointer()
          .border(1).border_color(t.border).tooltip(p.alt ? `${p.alt} · open the original` : "Open the original")
          .on_click(() => this.open(p.url))
          .child(image(`${CACHE}/${img.file}`).w(w).h(h));
      }
      // not a GitHub attachment (a Google Drive link, say), or no images at all: cardhub
      // downloaded none here (or isn't new enough to)
      if (!images || !(p.url in images) && !isAttachment(p.url) || img?.error || copied === "failed") {
        return h_flex().id(`${id}-img-${i}`).gap(6).items_center().text_size(11.5).text_color(t.muted).cursor_pointer()
          .hover((st) => st.text_color(t.foreground)).on_click(() => this.open(p.url))
          .child(svg(ICON("external")).size(11).flex_none())
          .child(div().min_w(0).truncate().child(`Picture: ${label}${img?.error ? ` (${img.error})` : ""}`));
      }
      return h_flex().id(`${id}-img-${i}`).gap(6).items_center().text_size(11.5).text_color(t.dim)
        .child(svg(ICON("spinner")).size(11).spin(900)).child(div().min_w(0).truncate().child(`Loading picture: ${label}`));
    }));
  }

  currentSuggestions() {
    if (!this.sugOpen || !this.state) return [];
    return suggestions(this.search.value(), this.state.suggest).slice(0, SUGGEST_MAX);
  }

  // Puts the highlighted suggestion (or the one given) into the box; a qualifier keeps the list
  // open for its values.
  acceptSuggestion(cx, item) {
    const it = item ?? this.currentSuggestions()[Math.max(0, this.sugIdx)];
    if (!it) return false;
    this.applying = true;
    this.search.set_value(it.apply);
    this.applying = false;
    this.search.set_selected_range({ start: it.apply.length, end: it.apply.length });
    this.search.focus();
    this.sugOpen = true;
    this.sugIdx = -1;
    cx.notify();
    return true;
  }

  searchKey(k, cx) {
    const list = this.currentSuggestions();
    if (!list.length) return;
    if (k === "down" || k === "ctrl-n") this.sugIdx = (this.sugIdx + 1) % list.length;
    else if (k === "up" || k === "ctrl-p") this.sugIdx = this.sugIdx <= 0 ? list.length - 1 : this.sugIdx - 1;
    else if (k === "tab") { this.acceptSuggestion(cx); return; }
    else if (k === "escape") this.sugOpen = false;
    else return;
    cx.notify();
  }

  suggestionList({ t, mono }, list, width) {
    return v_flex().id("sug-list").w(width).mt(4).p(4).gap(1).rounded(8).bg(t.surface).border(1).border_color(t.border).shadow_md()
      .children(list.map((it, i) => {
        const on = i === this.sugIdx;
        return h_flex().id(`sug-${i}`).h(26).px(8).gap(8).items_center().rounded(5).cursor_pointer()
          .bg(on ? tint(GH.blue, 0.18) : t.surface).hover((st) => st.bg(on ? tint(GH.blue, 0.18) : t.hover))
          .on_click((_e, cx) => this.acceptSuggestion(cx, it))
          .when(Boolean(it.color), (el) => el.child(div().size(8).rounded(4).flex_none().bg(it.color)))
          .child(div().flex_none().max_w(width * 0.55).truncate().font_family(mono).text_size(11.5).text_color(t.foreground).child(it.label))
          .child(div().flex_1().min_w(0).truncate().text_right().text_size(11).text_color(t.dim).child(it.hint ?? ""));
      }))
      .child(h_flex().px(8).pt(4).pb(2).gap(10).border_t(1).border_color(t.border).text_size(10.5).text_color(t.dim)
        .child("↑↓ choose").child("Tab or Enter pick").child("Esc close"));
  }

  // --- parts --------------------------------------------------------------------------------------
  render() {
    const t = theme();
    const ui = { t, mono: settings().font_family ?? settings().font ?? "JetBrains Mono" };
    const s = this.state;
    let body;
    if (!s) body = this.waiting(ui);
    else if (this.page) body = this.picker(ui, s);
    else if (s.detail) body = this.detail(ui, s, s.detail);
    else if (!s.project) body = this.picker(ui, s, "projects");
    else if (s.screen === "prs") body = this.prs(ui, s);
    else if (s.screen === "link") body = this.linkPage(ui, s);
    else if (s.screen === "issues") body = this.issues(ui, s);
    else body = this.board(ui, s);
    return v_flex().id("root").size_full().min_w(0).bg(t.background).text_color(t.foreground).text_size(12.5)
      .child(this.header(ui, s))
      .child(div().id("body").flex_1().min_h(0).flex().flex_col().child(body))
      .when(Boolean(s?.project) && !this.page, (el) => el.child(this.status(ui, s)))
      .when(this.alive === false || Boolean(s?.update), (el) => el.child(this.footer(ui, s)));
  }

  waiting({ t }) {
    return v_flex().size_full().items_center().justify_center().gap(10).text_color(t.muted)
      .child(svg(ICON("spinner")).size(18).spin(900))
      .child(div().child("Starting CardHub…"))
      .child(div().text_size(11).text_color(t.dim).max_w(320).text_center()
        .child("cardhub -gui runs in a hidden terminal and fills this panel once it has synced the board."));
  }

  // A banner over the status boxes: CardHub isn't running, or a release is out, with an Update
  // button as the terminal's U (then Restart, to run the new one).
  footer(ui, s) {
    const { t } = ui;
    const dead = this.alive === false;
    const u = s?.updating; // absent from a cardhub older than the button
    let color = dead ? GH.red : GH.blue, text, action = null;
    if (dead) text = "CardHub isn't running. This panel starts it again within 30 seconds.";
    else if (u?.busy) {
      text = h_flex().gap(6).items_center().child(svg(ICON("spinner")).size(12).spin(900)).child(`Updating to ${s.update}…`);
    } else if (u?.done) {
      color = GH.green;
      text = `CardHub ${u.done} is installed. Restart to use it.`;
      action = this.button(ui, "restart", "refresh", "Restart", (_e, cx) => this.restart(cx), { primary: true, color: GH.green });
    } else if (u?.error) {
      color = GH.red;
      text = `Update failed: ${u.error}`;
      action = this.button(ui, "update", "", "Try again", (_e, cx) => this.send(cx, { cmd: "update" }));
    } else if (u?.can) {
      text = `CardHub ${s.update} is out.`;
      action = this.button(ui, "update", "", "Update", (_e, cx) => this.send(cx, { cmd: "update" }), { primary: true, color: GH.blue, tip: "Download it, check it and replace this CardHub (and this panel)" });
    } else if (u) {
      text = `CardHub ${s.update} is out. This is a build from source: git pull, then make build.`;
    } else {
      text = `CardHub ${s.update} is out: run cardhub update, then restart it.`;
    }
    return h_flex().flex_none().mx(12).mb(12).px(12).py(8).gap(8).items_center().rounded(8)
      .bg(tint(color, 0.12)).border(1).border_color(tint(color, 0.4))
      .child(div().size(7).rounded(4).flex_none().bg(color))
      .child(div().flex_1().min_w(0).child(text))
      .when(Boolean(action), (el) => el.child(action));
  }

  // After an update: cardhub -gui quits, and the panel starts the new one as soon as it's gone
  // rather than at the next 30-second check.
  restart(cx) {
    this.send(cx, { cmd: "restart" });
    cx.spawn(async (cx) => {
      for (let i = 0; i < 20; i++) {
        await cx.sleep(500);
        if (!terminals().some((x) => x.name === "cardhub" && x.state === "live")) break;
      }
      await this.ensureNow(cx);
    });
  }


  // The terminal's footer boxes: Sync (with the refresh interval, pause and when the API budget
  // resets, in red once it's nearly gone) and AI link.
  status(ui, s) {
    const { t } = ui;
    const y = s.sync, l = s.link ?? {};
    const BLUE = GH.blue, AMBER = GH.amber;
    const box = (id, title, color, ...rows) => v_flex().id(id).when(id === "f-sync", (el) => el.flex_none()).when(id !== "f-sync", (el) => el.flex_1().min_w(190)).gap(6).px(10).py(8).rounded(8)
      .bg(t.surface).border(1).border_color(t.border)
      .child(h_flex().gap(8).items_center().child(div().text_size(11).font_semibold().text_color(color).child(title))
        .when(id === "f-sync" && Boolean(state), (el) => el.child(div().text_size(11).child(state))))
      .children(rows);
    const line = (...kids) => h_flex().gap(8).items_center().text_size(11.5).min_h(22).children(kids);
    const step = (id, text, d, disabled) => div().id(id).size(22).flex_none().rounded(5).flex().items_center().justify_center()
      .border(1).border_color(t.border).bg(t.raised).text_color(disabled ? t.dim : t.foreground).font_semibold()
      .when(!disabled, (el) => el.cursor_pointer().hover((st) => st.bg(t.hover)).on_click((_e, cx) => this.send(cx, { cmd: "interval", index: d })))
      .child(text);

    // Sync
    const state = y.fetching ? h_flex().gap(5).items_center().child(svg(ICON("spinner")).size(11).spin(900)).child("Syncing…")
      : y.paused ? div().font_semibold().text_color(AMBER).child("❚❚ Paused")
      : null;
    const resetMin = y.resetAt > Date.now() ? Math.ceil((y.resetAt - Date.now()) / 60000) : null;
    const resetIn = resetMin == null ? "" : resetMin >= 60 ? `${Math.floor(resetMin / 60)}h ${resetMin % 60}m` : `${resetMin}m`;
    const out = y.limit > 0 && y.remaining <= 0;
    const confirming = this.confirmPause;
    const pauseBtn = y.paused
      ? this.button(ui, "resume", "", "Resume", (_e, cx) => this.send(cx, { cmd: "pause" }), { tip: "Sync again, starting now" })
      : confirming
        ? h_flex().gap(4).items_center().child(div().text_size(11).text_color(AMBER).child("Pause syncing?"))
          .child(this.button(ui, "pause-yes", "", "Pause", (_e, cx) => { this.confirmPause = false; this.send(cx, { cmd: "pause" }); cx.notify(); }, { primary: true, color: AMBER }))
          .child(this.button(ui, "pause-no", "", "Cancel", (_e, cx) => { this.confirmPause = false; cx.notify(); }))
        : this.button(ui, "pause", "", "Pause", (_e, cx) => { this.confirmPause = true; cx.notify(); }, { tip: "Stop syncing the board (the AI link keeps running)" });
    const sync = box("f-sync", "Sync", BLUE,
      line(div().text_color(t.muted).child("Every"),
        h_flex().gap(3).items_center()
          .child(step("int-down", "−", -1, y.interval <= 5))
          .child(div().min_w(28).px(2).text_center().font_medium().child(`${y.interval}s`))
          .child(step("int-up", "+", 1, false)),
        div().w(4),
        pauseBtn),
      ...(y.low ? [h_flex().gap(7).items_start().px(8).py(6).rounded(6).bg(tint(GH.red, 0.12)).border(1).border_color(tint(GH.red, 0.45))
        .child(div().pt(4).size(7).rounded(4).flex_none().bg(GH.red))
        .child(v_flex().flex_1().min_w(0).gap(2).text_size(11.5)
          .child(div().font_semibold().text_color(GH.red).child(out ? "API budget used up" : `API budget almost used up: ${y.remaining} left`))
          .child(div().text_color(t.muted).child(out ? `Syncing stops until it resets${resetIn ? ` in ${resetIn}` : ""}.` : `Syncing slows down until it resets${resetIn ? ` in ${resetIn}` : ""}.`)))] : []),
      ...(y.error && s.board ? [div().text_size(11).text_color(GH.red).child(y.error)] : []));

    // AI link
    const parts = [];
    if (l.boards > 1) parts.push(plural(l.boards, "board"));
    if (l.boards === 1 && !l.rules && !l.sent) parts.push("ready", "0 rules");
    else parts.push(plural(l.rules ?? 0, "rule"), `${l.sent ?? 0} sent`);
    if (l.queued) parts.push(`${l.queued} queued`);
    const waiting = l.waiting > 0;
    const link = box("f-link", "AI link", AMBER,
      ...(l.conflict ? [div().text_size(11.5).font_semibold().text_color(AMBER).child(`⚠ ${l.conflict}`)] : []),
      l.boards ? line(div().text_color(t.muted).child(parts.join(" · ")),
        div().flex_1(),
        div().size(8).rounded(4).flex_none().bg(waiting ? GH.green : l.queued ? AMBER : t.dim),
        div().child(waiting ? (l.waiting > 1 ? `${l.waiting} agents waiting` : "agent waiting") : "no agent waiting"))
        : line(div().text_color(t.dim).child("Not running")));

    return h_flex().id("status").flex_none().flex_wrap().items_stretch().gap(8).px(12).pt(8).pb(12).border_t(1).border_color(t.border)
      .child(sync).when(s.screen !== "link", (el) => el.child(link));
  }

  button({ t }, id, icon, label, action, { tip, primary = false, disabled = false, color } = {}) {
    const fg = disabled ? t.dim : primary ? "#ffffff" : color ?? t.foreground;
    return h_flex().id(id).flex_none().h(26).px(label ? 9 : 6).gap(5).items_center().rounded(6).text_size(11.5).font_medium()
      .border(1).border_color(primary ? (color ?? GH.green) : t.border)
      .bg(primary ? (disabled ? tint(color ?? GH.green, 0.4) : color ?? GH.green) : t.raised).text_color(fg)
      .when(!disabled, (el) => el.cursor_pointer().hover((st) => st.opacity(0.85)).on_click(action))
      .when(Boolean(tip), (el) => el.tooltip(tip))
      .when(Boolean(icon), (el) => el.child(svg(ICON(icon)).size(12)))
      .when(Boolean(label), (el) => el.child(label));
  }

  badge(color, text, solid = false) {
    return div().flex_none().h(18).px(6).rounded(5).flex().items_center().text_size(10.5).font_medium()
      .bg(tint(color, solid ? 0.9 : 0.16)).text_color(solid ? "#ffffff" : color).child(text);
  }

  segment({ t }, id, items, active, onPick) {
    return h_flex().flex_none().h(28).p(2).gap(2).rounded(7).bg(t.hover).children(items.map((it) =>
      h_flex().id(`${id}-${it.key}`).h_full().px(10).gap(5).items_center().rounded(5).text_size(11.5).font_medium()
        .bg(it.key === active ? t.background : t.hover).text_color(it.key === active ? t.foreground : t.muted)
        .when(it.key !== active, (el) => el.cursor_pointer().hover((st) => st.text_color(t.foreground)).on_click((_e, cx) => onPick(cx, it.key)))
        .when(Boolean(it.icon), (el) => el.child(svg(ICON(it.icon)).size(12)))
        .child(it.label)
        .when(it.count != null, (el) => el.child(div().text_color(t.dim).child(String(it.count))))));
  }

  header(ui, s) {
    const { t } = ui;
    const sync = s?.sync;
    const status = !s ? null
      : sync.fetching ? h_flex().gap(5).items_center().text_color(t.muted).child(svg(ICON("spinner")).size(11).spin(900)).child("Syncing")
      : sync.error ? h_flex().gap(5).items_center().text_color(GH.red).child(div().size(7).rounded(4).bg(GH.red)).child("Sync failed").tooltip(sync.error)
      : sync.fetchedAt ? h_flex().gap(5).items_center().text_color(t.muted).child(div().size(7).rounded(4).bg(GH.green))
        .child(ago(sync.fetchedAt)).tooltip(`Syncs every ${sync.every}s · API ${sync.remaining}/${sync.limit} left`)
      : null;
    const title = s?.project ? (s.project.title || `${s.project.owner}/${s.project.number}`) : "CardHub";
    return v_flex().flex_none().border_b(1).border_color(t.border)
      .child(h_flex().h(48).px(12).gap(10).items_center()
        .child(div().size(28).flex_none().rounded(7).flex().items_center().justify_center().bg(tint(GH.blue, 0.15))
          .text_color(GH.blue).child(svg(ICON("board")).size(16)))
        .child(h_flex().id("project").min_w(0).flex_1().gap(5).items_center().cursor_pointer()
          .on_click((_e, cx) => this.openPage(cx, "projects"))
          .tooltip("Switch project")
          .child(div().min_w(0).truncate().font_semibold().text_size(14).child(title))
          .child(svg(ICON("chevron-down")).size(12).flex_none().text_color(t.muted)))
        .when(Boolean(s?.version), (el) => el.child(div().flex_none().text_size(11.5).text_color(t.dim).child(s.version)))
        .when(Boolean(status), (el) => el.child(div().flex_none().text_size(11.5).child(status)))
        .when(Boolean(s?.project), (el) => el
          .child(this.button(ui, "refresh", "refresh", "", (_e, cx) => this.send(cx, { cmd: "refresh" }), { tip: "Sync now" }))
          .child(this.button(ui, "open-gh", "external", "", () => this.open(s.screen === "prs" && s.prs.repo
            ? `https://github.com/${s.prs.repo}/pulls` : s.screen === "issues" && s.prs.repo
            ? `https://github.com/${s.prs.repo}/issues` : s.project.url), { tip: "Open on GitHub" }))))
      .when(Boolean(s?.project), (el) => el.child(h_flex().h(38).px(12).gap(8).items_center()
        .child(this.segment(ui, "screen", [
          { key: "board", label: "Board", icon: "board" },
          { key: "prs", label: "Pull requests", icon: "pr" },
          { key: "issues", label: "Issues", icon: "issue" },
          { key: "link", label: "AI link", icon: "link" },
        ], this.page || s.detail ? null : s.screen, (cx, key) => { this.page = null; if (s.detail) this.send(cx, { cmd: "close" }); this.send(cx, { cmd: "screen", screen: key }); }))
        .child(div().flex_1())
        .when(s.screen === "board" && !s.detail && !this.page && s.board, (el) => el.child(div().text_color(t.muted).text_size(11.5)
          .child(plural(s.board.table ? s.board.rows : s.board.columns.reduce((n, c) => n + c.total, 0), "item"))))));
  }

  // --- board --------------------------------------------------------------------------------------
  board(ui, s) {
    const { t } = ui;
    const views = [{ index: -1, name: "All items" }, ...(s.views ?? [])];
    const chips = h_flex().id("views").flex_none().h(34).px(12).pt(10).gap(4).overflow_x_scrollbar().children(views.map((v) => {
      const on = v.index === s.viewIdx;
      return h_flex().id(`view-${v.index}`).flex_none().h(24).px(9).gap(5).items_center().rounded(6).text_size(11.5)
        .bg(on ? tint(GH.blue, 0.16) : t.background).border(1).border_color(on ? tint(GH.blue, 0.55) : t.border)
        .text_color(on ? t.foreground : t.muted)
        .when(!on, (el) => el.cursor_pointer().hover((st) => st.bg(t.hover)).on_click((_e, cx) => this.send(cx, { cmd: "view", index: v.index })))
        .when(v.table, (el) => el.child(div().text_color(t.dim).child("▦")))
        .child(v.name);
    }));
    const sug = this.currentSuggestions();
    if (this.sugIdx >= sug.length) this.sugIdx = sug.length - 1;
    const searchBox = h_flex().id("search-box").flex_none().h(30).px(9).gap(7).items_center().rounded(7)
      .border(1).border_color(t.input).bg(t.surface)
      .key_context("CardHubSearch")
      .on_action("cardhub-sug-up", (_e, cx) => this.searchKey("up", cx))
      .on_action("cardhub-sug-down", (_e, cx) => this.searchKey("down", cx))
      .on_action("cardhub-sug-ctrl-p", (_e, cx) => this.searchKey("up", cx))
      .on_action("cardhub-sug-ctrl-n", (_e, cx) => this.searchKey("down", cx))
      .on_action("cardhub-sug-tab", (_e, cx) => this.searchKey("tab", cx))
      .on_action("cardhub-sug-escape", (_e, cx) => this.searchKey("escape", cx))
      .on_size_change((sz, cx) => { if (Math.abs((this.searchW ?? 0) - sz.width) > 1) { this.searchW = sz.width; cx.notify(); } })
      .child(svg(ICON("search")).size(13).text_color(t.dim).flex_none())
      .child(Input.new(this.search).flex_1().min_w(0).h_full().text_size(12).text_color(t.foreground))
      .when(Boolean(s.search), (el) => el.child(div().id("clear-search").flex_none().text_color(t.dim).cursor_pointer()
        .hover((st) => st.text_color(t.foreground))
        .on_click((_e, cx) => { this.search.set_value(""); this.send(cx, { cmd: "search", text: "" }); })
        .child(svg(ICON("x")).size(12))));
    const search = Popup.new("search-sug", searchBox).anchor("top_left").flex_none().mx(12).mt(8).mb(10)
      .when(sug.length > 0 && Boolean(this.searchW), (el) => el.content(this.suggestionList(ui, sug, this.searchW)));
    const b = s.board;
    let content;
    if (!b) content = this.empty(ui, s.sync.error ? s.sync.error : "Loading the board…", !s.sync.error);
    else if (b.table) content = this.table(ui, b);
    else content = this.grid(ui, b.columns);
    return v_flex().flex_1().min_h(0).child(chips).child(search).child(content);
  }

  // The board as in the terminal: columns in a grid that wraps to the panel's width, every box of a
  // row as tall as the others and the rows sharing the height; a box scrolls its own cards.
  grid(ui, cols) {
    const { t } = ui;
    const GAP = 50, ROW_GAP = 24, MIN_W = 230, MIN_H = 190, PAD = 12;
    const size = this.gridSize;
    const area = v_flex().id("grid").flex_1().min_h(0)
      .on_size_change((sz, cx) => {
        if (!this.gridSize || Math.abs(sz.width - this.gridSize.width) > 1 || Math.abs(sz.height - this.gridSize.height) > 1) {
          this.gridSize = { width: sz.width, height: sz.height }; cx.notify();
        }
      });
    if (!size) return area;
    const n = cols.length;
    if (!n) return area.child(this.empty(ui, "No items match"));
    const w = size.width - 2 * PAD, h = size.height - PAD;
    const fit = Math.max(1, Math.min(n, Math.floor((w + GAP) / (MIN_W + GAP))));
    const boxW = Math.floor((w - (fit - 1) * GAP) / fit);
    const rows = Math.ceil(n / fit);
    const boxH = Math.max(MIN_H, Math.floor((h - (rows - 1) * ROW_GAP) / rows));
    const lines = [];
    for (let r = 0; r < rows; r++) {
      const row = h_flex().id(`grow-${r}`).flex_none().h(boxH).items_center();
      for (let c = 0; c < fit; c++) {
        const i = r * fit + c;
        if (i >= n) break;
        if (c > 0) row.child(this.flow(ui, `hf-${i}`, cols[i].color, GAP, this.state.board.flows?.[i - 1]));
        row.child(this.column(ui, cols[i], i, boxW, boxH));
      }
      lines.push(row);
      if (r < rows - 1) lines.push(this.wrapFlow(ui, `wf-${r}`, cols[(r + 1) * fit].color, fit, boxW, GAP, ROW_GAP, this.state.board.flows?.[(r + 1) * fit - 1]));
    }
    return area.child(v_flex().id("grid-scroll").size_full().px(PAD).pb(PAD).overflow_y_scrollbar().children(lines));
  }

  // A lane of dots, animated like the terminal's (natively, with frames: no timer here): bright
  // dots march over dim ones toward dir, and on a lane a card crossed this session a white dot
  // streams along it in the latest move's direction.
  lane({ t }, id, cells, dir, color, busy) {
    const CELL = 9, TICK = 150;
    const march = (i) => [0, 1, 2].map((f) => (((i - dir * f) % 3) + 3) % 3 === 0 ? "•" : " ");
    const len = cells * 3, step = Math.max(1, Math.floor(cells / 8));
    const white = (i) => Array.from({ length: len }, (_, f) => {
      let pos = (f * step) % cells;
      if (busy.dir < 0) pos = cells - 1 - pos;
      return pos === i ? "•" : " ";
    });
    return h_flex().id(id).flex_none().items_center().children(Array.from({ length: cells }, (_, i) =>
      div().id(`${id}-${i}`).relative().w(CELL).h(16).flex_none()
        .child(div().absolute().top(0).left(0).w(CELL).h(16).flex().items_center().justify_center().text_size(16).font_bold().text_color(t.muted).child("·"))
        .child(div().absolute().top(0).left(0).w(CELL).h(16).flex().items_center().justify_center().text_size(12).text_color(color).frames(march(i), TICK))
        .when(busy.moves > 0, (el) => el.child(div().absolute().top(0).left(0).w(CELL).h(16).flex().items_center().justify_center().text_size(12).font_bold().text_color("#ffffff").frames(white(i), TICK)))));
  }

  // The connector between two boxes of a row: ··•·▸ toward the next column (◂ when the latest
  // move across it went back).
  flow({ t }, id, color, w, traffic) {
    const busy = traffic ?? { moves: 0, dir: 1 };
    const back = busy.moves > 0 && busy.dir < 0;
    const head = div().flex_none().h(16).flex().items_center().text_size(12).text_color(color).child(back ? "◂" : "▸");
    return h_flex().id(id).w(w).flex_none().justify_center().items_center()
      .when(back, (el) => el.child(head))
      .child(this.lane({ t }, `${id}-l`, 4, back ? -1 : 1, color, busy))
      .when(!back, (el) => el.child(head));
  }

  // The connector from a row's last box back to the next row's first, as in the terminal: ╭····╯,
  // its dots running right to left (forward). With one box per row it's a blinking ▼.
  wrapFlow({ t }, id, color, fit, boxW, gap, h, traffic) {
    const busy = traffic ?? { moves: 0, dir: 1 };
    if (fit === 1) {
      return h_flex().id(id).flex_none().h(h).w(boxW).justify_center().items_center()
        .child(div().text_size(12).text_color(color).frames(busy.dir < 0 ? ["▲", "△"] : ["▼", "▽"], 300));
    }
    const span = (fit - 1) * (boxW + gap);
    const cells = Math.max(1, Math.floor((span - 18) / 9));
    return h_flex().id(id).flex_none().h(h).items_center().pl(boxW / 2 - 4)
      .child(div().w(9).h(16).pt(3).flex().items_center().justify_center().text_size(12).text_color(color).child("╭"))
      .child(this.lane({ t }, `${id}-l`, cells, -1, color, { moves: busy.moves, dir: -busy.dir }))
      .child(div().w(9).h(16).pt(2).flex().items_center().justify_center().text_size(12).text_color(t.muted).child("╯"));
  }

  column(ui, c, i, w, h) {
    const { t } = ui;
    const shown = c.cards.length;
    return v_flex().id(`col-${i}`).w(w).h(h).flex_none().rounded(8).bg(t.background).border(1).border_color(t.border)
      .child(h_flex().flex_none().h(30).px(10).gap(7).items_center()
        .child(div().text_color(c.color).text_size(11).child("●"))
        .child(div().flex_1().min_w(0).truncate().font_semibold().child(c.name))
        .child(div().flex_none().text_size(11.5).text_color(t.muted).child(String(c.total))))
      .child(div().flex_none().mx(10).h(1).bg(tint(c.color, 0.45)))
      .child(v_flex().id(`cards-${i}`).flex_1().min_h(0).px(10).py(8).gap(10).overflow_y_scrollbar()
        .children(shown ? c.cards.map((card) => this.card(ui, card, c.color)) : [div().pt(4).text_color(t.dim).text_size(11.5).child("No cards")]))
      .when(c.total > 0, (el) => el.child(h_flex().flex_none().h(24).px(10).gap(8).items_center().text_size(11).text_color(t.dim)
        .child(div().flex_1().child(`${plural(c.total, "card")}${shown < c.total ? ` · ${shown} loaded` : ""}`))
        .when(c.more, (el) => el.child(div().id(`more-${i}`).text_color(t.muted).cursor_pointer().hover((st) => st.text_color(t.foreground))
          .on_click((_e, cx) => this.send(cx, { cmd: "more", column: c.name }))
          .child(`${c.total - shown} more ▼`)))));
  }

  // A card as the terminal draws it: a bar in the column's colour, then the title, the ref and state,
  // and the people and labels, one line each.
  card(ui, card, color) {
    const { t } = ui;
    const who = [...(card.assignees ?? []).map((p) => `@${p}`), ...(card.labels ?? []).map((l) => `[${l}]`)].join(" ");
    const ref = card.kind === "DRAFT_ISSUE" ? "draft" : shortRef(card.ref);
    return h_flex().id(`card-${card.id}`).flex_none().gap(7).items_start().rounded(4).cursor_pointer()
      .hover((st) => st.bg(t.hover))
      .on_click((_e, cx) => this.send(cx, { cmd: "card", id: card.id }))
      .child(div().w(3).flex_none().h(46).rounded(1).bg(color))
      .child(v_flex().flex_1().min_w(0).gap(1)
        .child(h_flex().gap(5).items_center()
          .when(card.changed, (el) => el.child(div().flex_none().text_size(9).text_color(GH.amber).child("●")))
          .child(div().flex_1().min_w(0).truncate().font_semibold().text_size(12).child(card.title)))
        .child(div().truncate().text_size(11).text_color(t.muted).font_family(ui.mono)
          .child(`${ref} · ${(card.state ?? "").toLowerCase()}`))
        .child(div().truncate().text_size(11).text_color(t.muted).child(who || " ")));
  }

  table(ui, b) {
    const { t } = ui;
    const cols = (b.fields ?? []).map((f, i) => [f, i]).filter(([f]) => f !== "Title");
    const rows = (b.items ?? []).map((it) => v_flex().id(`row-${it.id}`).flex_none().px(10).py(8).gap(5).rounded(8)
      .bg(t.background).border(1).border_color(it.changed ? tint(GH.amber, 0.7) : t.border).cursor_pointer()
      .hover((st) => st.bg(t.hover))
      .on_click((_e, cx) => this.send(cx, { cmd: "card", id: it.id }))
      .child(h_flex().gap(7).items_center()
        .child(div().size(7).rounded(4).flex_none().bg(stateColor(it.state)))
        .child(div().flex_1().min_w(0).font_medium().child(it.title))
        .child(div().flex_none().text_size(11).text_color(t.dim).child(shortRef(it.ref))))
      .child(h_flex().flex_wrap().gap(4)
        .when(!cols.some(([f]) => f === "Assignees") && (it.assignees ?? []).length > 0, (el) => el.child(
          h_flex().h(20).px(6).gap(4).items_center().rounded(5).bg(t.hover).text_size(11)
            .child(div().text_color(t.dim).child("Assignees")).child(div().text_color(t.foreground).child(it.assignees.map((a) => `@${a}`).join(" ")))))
        .children(cols.filter(([, i]) => it.cells?.[i]).map(([f, i]) =>
          h_flex().h(20).px(6).gap(4).items_center().rounded(5).bg(t.hover).text_size(11)
            .child(div().text_color(t.dim).child(f)).child(div().text_color(t.foreground).child(it.cells[i]))))));
    return v_flex().id("table").flex_1().min_h(0).px(12).pb(12).gap(6).overflow_y_scrollbar()
      .children(rows.length ? rows : [this.empty(ui, "No items")])
      .when((b.items?.length ?? 0) < Math.min(b.rows, 100), (el) => el.child(div().id("table-more").flex_none().h(28).rounded(7).flex().items_center()
        .justify_center().text_size(11.5).text_color(t.muted).border(1).border_dashed().border_color(t.border).cursor_pointer()
        .hover((st) => st.bg(t.hover)).on_click((_e, cx) => this.send(cx, { cmd: "more", column: "" }))
        .child(`Show more · ${b.rows - b.items.length} left`)));
  }

  empty({ t }, text, spin = false) {
    return v_flex().flex_1().min_h(120).items_center().justify_center().gap(8).px(20).text_color(t.muted).text_center()
      .when(spin, (el) => el.child(svg(ICON("spinner")).size(16).spin(900)))
      .child(div().max_w(420).child(text));
  }

  // --- pull requests ------------------------------------------------------------------------------
  prs(ui, s) {
    const { t } = ui;
    const p = s.prs;
    const bar = h_flex().flex_none().px(12).pt(10).pb(8).gap(8).items_center()
      .child(h_flex().id("repo").min_w(0).h(28).px(9).gap(6).items_center().rounded(7).border(1).border_color(t.border)
        .bg(t.surface).cursor_pointer().hover((st) => st.bg(t.hover)).on_click((_e, cx) => this.openPage(cx, "repos"))
        .tooltip("Switch repo")
        .child(svg(ICON("pr")).size(12).text_color(t.muted).flex_none())
        .child(div().min_w(0).truncate().font_medium().child(p.repo || "Pick a repo"))
        .child(svg(ICON("chevron-down")).size(11).text_color(t.muted).flex_none()))
      .child(div().flex_1())
      .when(Boolean(p.repo), (el) => el.child(this.segment(ui, "state", [
        { key: "open", label: "Open", count: p.open },
        { key: "closed", label: "Closed", count: p.closedCount },
      ], p.closed ? "closed" : "open", (cx, key) => this.send(cx, { cmd: "prState", closed: key === "closed" }))));
    let content;
    if (!p.repo) content = this.empty(ui, "Pick the repo whose pull requests this screen shows.");
    else if (p.error) content = this.empty(ui, p.error);
    else if (!p.list.length) content = this.empty(ui, p.loading ? "Loading pull requests…" : p.closed ? "No closed pull requests." : "No open pull requests.", p.loading);
    else content = v_flex().id("pr-list").flex_1().min_h(0).px(12).pb(12).gap(6).overflow_y_scrollbar()
      .children(p.list.map((pr) => this.prRow(ui, pr)))
      .when((p.closed ? p.closedCount : p.open) > p.list.length, (el) => el.child(div().id("prs-all").flex_none().h(28).flex().items_center()
        .justify_center().text_size(11.5).text_color(t.muted).cursor_pointer().hover((st) => st.text_color(t.foreground))
        .on_click(() => this.open(`https://github.com/${p.repo}/pulls${p.closed ? "?q=is%3Apr+is%3Aclosed" : ""}`))
        .child(`Latest ${p.list.length} of ${p.closed ? p.closedCount : p.open} · all on GitHub`)));
    return v_flex().flex_1().min_h(0).child(bar)
      .when(Boolean(p.flash), (el) => el.child(h_flex().flex_none().mx(12).mb(8).px(10).py(6).gap(8).rounded(7)
        .bg(tint(GH.purple, 0.14)).border(1).border_color(tint(GH.purple, 0.45)).child(svg(ICON("merge")).size(13).text_color(GH.purple)).child(p.flash)))
      .child(content);
  }

  // --- issues -------------------------------------------------------------------------------------
  // The repo's issues, open or closed: the same repo as the Pull requests screen (its menu switches both).
  issues(ui, s) {
    const { t } = ui;
    const p = s.prs, is = s.issues ?? { list: [] };
    const bar = h_flex().flex_none().px(12).pt(10).pb(8).gap(8).items_center()
      .child(h_flex().id("issue-repo").min_w(0).h(28).px(9).gap(6).items_center().rounded(7).border(1).border_color(t.border)
        .bg(t.surface).cursor_pointer().hover((st) => st.bg(t.hover)).on_click((_e, cx) => this.openPage(cx, "repos"))
        .tooltip("Switch repo (also for pull requests)")
        .child(svg(ICON("issue")).size(12).text_color(t.muted).flex_none())
        .child(div().min_w(0).truncate().font_medium().child(p.repo || "Pick a repo"))
        .child(svg(ICON("chevron-down")).size(11).text_color(t.muted).flex_none()))
      .child(div().flex_1())
      .when(Boolean(p.repo), (el) => el.child(this.segment(ui, "istate", [
        { key: "open", label: "Open", count: is.open },
        { key: "closed", label: "Closed", count: is.closedCount },
      ], is.closed ? "closed" : "open", (cx, key) => this.send(cx, { cmd: "issueState", closed: key === "closed" }))));
    let content;
    if (!p.repo) content = this.empty(ui, "Pick the repo whose issues this screen shows.");
    else if (is.error) content = this.empty(ui, is.error);
    else if (!is.list.length) content = this.empty(ui, is.loading ? "Loading issues…" : is.closed ? "No closed issues." : "No open issues.", is.loading);
    else content = v_flex().id("issue-list").flex_1().min_h(0).px(12).pb(12).gap(6).overflow_y_scrollbar()
      .children(is.list.map((x) => this.issueRow(ui, x)))
      .when((is.closed ? is.closedCount : is.open) > is.list.length, (el) => el.child(div().id("issues-all").flex_none().h(28).flex().items_center()
        .justify_center().text_size(11.5).text_color(t.muted).cursor_pointer().hover((st) => st.text_color(t.foreground))
        .on_click(() => this.open(`https://github.com/${p.repo}/issues${is.closed ? "?q=is%3Aissue+is%3Aclosed" : ""}`))
        .child(`Latest ${is.list.length} of ${is.closed ? is.closedCount : is.open} · all on GitHub`)));
    return v_flex().flex_1().min_h(0).child(bar).child(content);
  }

  issueRow(ui, x) {
    const { t } = ui;
    const open = x.state === "OPEN";
    const skipped = x.reason === "NOT_PLANNED" || x.reason === "DUPLICATE";
    const color = open ? GH.green : skipped ? GH.gray : GH.purple;
    const icon = open ? "issue" : skipped ? "issue-skip" : "issue-closed";
    return h_flex().id(`issue-${x.number}`).flex_none().px(10).py(8).gap(10).items_start().rounded(8).bg(t.background)
      .border(1).border_color(t.border).cursor_pointer().hover((st) => st.bg(t.hover))
      .on_click((_e, cx) => this.send(cx, { cmd: "issue", number: x.number }))
      .child(div().pt(1).flex_none().text_color(color).child(svg(ICON(icon)).size(15)))
      .child(v_flex().flex_1().min_w(0).gap(4)
        .child(div().min_w(0).font_medium().child(x.title))
        .child(h_flex().flex_wrap().gap(6).items_center().text_size(11).text_color(t.muted)
          .child(div().child(`#${x.number} by @${x.author}`))
          .children((x.labels ?? []).map((l) => this.badge(t.muted, l)))
          .when(Boolean(x.milestone), (el) => el.child(div().text_color(t.dim).child(`◷ ${x.milestone}`)))))
      .child(v_flex().flex_none().items_end().gap(4).text_size(11)
        .when((x.assignees ?? []).length > 0, (el) => el.child(div().text_color(t.muted).child(x.assignees.map((a) => `@${a}`).join(" "))))
        .child(h_flex().gap(8).items_center()
          .when(x.comments > 0, (el) => el.child(h_flex().gap(3).items_center().text_color(t.muted).child(svg(ICON("comment")).size(11)).child(String(x.comments))))
          .child(div().text_color(t.dim).child(ago(open ? x.updated : x.closedAt || x.updated)))));
  }

  prRow(ui, pr) {
    const { t } = ui;
    const review = REVIEW[pr.review], checks = CHECKS[pr.checks];
    const icon = pr.state === "MERGED" ? "merge" : "pr";
    return h_flex().id(`pr-${pr.number}`).flex_none().px(10).py(8).gap(10).items_start().rounded(8).bg(t.background)
      .border(1).border_color(t.border).cursor_pointer().hover((st) => st.bg(t.hover))
      .on_click((_e, cx) => this.send(cx, { cmd: "pr", number: pr.number }))
      .child(div().pt(1).flex_none().text_color(prColor(pr)).child(svg(ICON(icon)).size(15)))
      .child(v_flex().flex_1().min_w(0).gap(4)
        .child(h_flex().gap(6).items_center()
          .child(div().min_w(0).font_medium().child(pr.title))
          .when(pr.draft, (el) => el.child(this.badge(GH.gray, "Draft"))))
        .child(h_flex().flex_wrap().gap(8).items_center().text_size(11).text_color(t.muted)
          .child(div().child(`#${pr.number} by @${pr.author}`))
          .child(div().text_color(t.dim).child(`${pr.head} → ${pr.base}`))
          .when(Boolean(review), (el) => el.child(this.badge(review[1], review[0])))
          .when(Boolean(checks), (el) => el.child(div().text_color(checks[1]).child(checks[0])))
          .when(pr.comments > 0, (el) => el.child(h_flex().gap(3).items_center().child(svg(ICON("comment")).size(11)).child(String(pr.comments))))))
      .child(v_flex().flex_none().items_end().gap(4).text_size(11)
        .child(h_flex().gap(4).child(div().text_color(GH.green).child(`+${pr.additions}`)).child(div().text_color(GH.red).child(`−${pr.deletions}`)))
        .child(div().text_color(t.dim).child(ago(pr.merged || pr.closed || pr.updated))));
  }

  // --- AI link ------------------------------------------------------------------------------------
  // The terminal's screen 4: who watches which board, the rules agents added, how to connect an
  // agent (until one has), and one activity timeline.
  linkPage(ui, s) {
    const { t, mono } = ui;
    const pg = s.linkPage ?? { watchers: [], activity: [], prompt: "" };
    const TEAL = "#39c5cf";
    const active = pg.watchers.filter((w) => w.active);
    const rules = pg.watchers.flatMap((w) => w.rules.map((r) => ({ ...r, board: w.title || w.board })));
    const sum = (f) => pg.watchers.reduce((n, w) => n + f(w), 0);
    const matching = rules.reduce((n, r) => n + r.matches, 0), queued = sum((w) => w.queued), sent = sum((w) => w.sent);
    const card = (id, title, color, children, action) => v_flex().id(id).flex_none().gap(8).p(12).rounded(8).bg(t.surface).border(1).border_color(color ?? t.border)
      .child(h_flex().gap(8).items_center().child(div().flex_1().font_semibold().child(title)).when(Boolean(action), (el) => el.child(action)))
      .children(children);

    // Watchers
    const conflicts = pg.watchers.filter((w) => w.onScreen && w.conflict).map((w, i) =>
      h_flex().id(`lc-${i}`).gap(6).items_center().text_size(11.5).text_color(GH.amber).child(div().font_semibold().child("⚠")).child(w.conflict));
    const watcherRows = active.map((w, i) => {
      const stopping = this.stopWatcher === w.board;
      return h_flex().id(`lw-${i}`).gap(10).items_center().px(10).py(8).rounded(7).bg(t.background).border(1).border_color(w.waiting ? tint(GH.purple, 0.6) : t.border)
        .child(v_flex().flex_1().min_w(0).gap(2)
          .child(h_flex().gap(6).items_center()
            .child(div().min_w(0).truncate().font_semibold().child(w.title || w.board))
            .when(w.onScreen, (el) => el.child(this.badge(GH.blue, "on screen"))))
          .child(h_flex().gap(6).items_center().text_size(11).text_color(t.muted)
            .when(Boolean(w.title), (el) => el.child(div().font_family(mono).child(w.board)))
            .child(div().child(`${plural(w.rules.length, "rule")} · ${w.sent} sent`))
            .when(w.queued > 0, (el) => el.child(div().text_color(GH.amber).child(`· ${w.queued} queued`)))))
        .child(h_flex().flex_none().gap(5).items_center().text_size(11.5)
          .child(div().size(8).rounded(4).bg(w.waiting ? GH.green : w.queued ? GH.amber : t.dim))
          .child(div().text_color(w.waiting ? t.foreground : t.muted).child(w.waiting ? "agent waiting" : "no agent")))
        .child(stopping
          ? h_flex().flex_none().gap(4).items_center()
            .child(this.button(ui, `lw-stop-yes-${i}`, "", "Stop", (_e, cx) => { this.stopWatcher = null; this.send(cx, { cmd: "stopWatcher", text: w.board }); cx.notify(); }, { primary: true, color: GH.red }))
            .child(this.button(ui, `lw-stop-no-${i}`, "", "Cancel", (_e, cx) => { this.stopWatcher = null; cx.notify(); }))
          : this.button(ui, `lw-stop-${i}`, "x", "", (_e, cx) => { this.stopWatcher = w.board; cx.notify(); },
            { tip: w.waiting ? "Stop this watcher: the waiting agent is told CardHub stopped" : "Stop this watcher" }));
    });
    const watchers = card("l-watchers", `Watchers (${active.length})`, conflicts.length ? tint(GH.amber, 0.6) : undefined, [
      ...conflicts,
      ...(watcherRows.length ? watcherRows : [div().text_size(11.5).text_color(t.muted).child("No watchers running. Open a project, or let an agent run cardhub notify add --board \"<project name>\".")]),
      ...(active.length ? [div().text_size(11).text_color(t.dim).child("Switching project or pausing the board doesn't stop a watcher.")] : []),
    ]);

    // Tiles
    const tile = (id, title, n, unit, color) => v_flex().id(id).flex_1().min_w(110).gap(2).px(12).py(9).rounded(8).bg(t.surface).border(1).border_color(t.border)
      .child(div().text_size(11).text_color(t.muted).child(title))
      .child(h_flex().gap(5).items_end().child(div().text_size(20).font_semibold().text_color(n ? color : t.dim).child(String(n))).child(div().pb(3).text_size(11).text_color(t.dim).child(unit)));
    const tiles = h_flex().flex_wrap().gap(8)
      .child(tile("lt-rules", "Rules", rules.length, "active", GH.blue))
      .child(tile("lt-match", "Matching", matching, "cards", t.foreground))
      .child(tile("lt-queued", "Queued", queued, "to send", GH.amber))
      .child(tile("lt-sent", "Sent", sent, "total", TEAL));

    // Rules
    const several = active.length > 1;
    const ruleRows = rules.map((r, i) => h_flex().id(`lr-${i}`).gap(10).items_center().px(10).py(7).rounded(6).bg(t.background).border(1).border_color(t.border).text_size(11.5)
      .child(div().w(22).flex_none().font_family(mono).text_color(GH.blue).child(`#${r.id}`))
      .when(several, (el) => el.child(div().w(110).flex_none().truncate().child(r.board)))
      .child(div().flex_1().min_w(0).font_family(mono).text_size(11).child(r.filter))
      .child(v_flex().flex_none().items_end().child(div().font_semibold().child(String(r.matches))).child(div().text_size(10).text_color(t.dim).child("matching")))
      .child(v_flex().w(56).flex_none().items_end().child(div().font_semibold().text_color(TEAL).child(String(r.reported))).child(div().text_size(10).text_color(t.dim).child("reported"))));
    const rulesCard = card("l-rules", `Rules (${rules.length})`, undefined, ruleRows.length ? ruleRows
      : [h_flex().gap(4).text_size(11.5).text_color(t.muted).child("No rules yet. An agent adds one with").child(div().font_family(mono).text_color(t.foreground).child("cardhub notify add '<filter>'"))]);

    // Connect an agent (until one has)
    const step = (n, cmd, what) => h_flex().gap(8).items_start().text_size(11.5)
      .child(div().w(14).flex_none().font_semibold().text_color(GH.blue).child(n))
      .child(v_flex().flex_1().min_w(0).gap(1).child(div().font_family(mono).text_size(11).child(cmd)).child(div().text_color(t.muted).child(what)));
    const copied = this.copiedAt && Date.now() - this.copiedAt < 3000;
    const connect = card("l-connect", "Connect an agent", undefined, [
      step("1", "cardhub notify add '<filter>'", "report cards matching a GitHub project filter"),
      step("2", "cardhub events --wait --json", "in the background: wakes the agent on each report"),
      div().text_size(11).text_color(t.dim).child("Quitting CardHub stops the watchers, and a waiting agent is told (exit code 3)."),
      div().pt(4).text_size(11.5).text_color(t.muted).child("Or paste this sample prompt into your agent:"),
      h_flex().gap(8).px(10).py(8).rounded(6).bg(t.background).border_l(3).border_color(GH.blue)
        .child(div().flex_1().min_w(0).text_size(11.5).child(pg.prompt)),
      div().text_size(11).text_color(t.dim).child("With several boards watched, add --board \"<project name>\" to both commands."),
    ], this.button(ui, "copy-prompt", "", copied ? "Copied ✓" : "Copy prompt", (_e, cx) => {
      exec("sh", ["-c", "printf %s \"$1\" | pbcopy", "sh", pg.prompt]).catch(() => {});
      this.copiedAt = Date.now(); cx.notify();
      cx.spawn(async (cx) => { await cx.sleep(3100); cx.notify(); });
    }, { tip: "Copy the sample prompt to the clipboard" }));

    // Activity
    const ICONS = { rule: ["✚", GH.blue], queued: ["◆", GH.amber], sent: ["➜", TEAL], agent: ["◉", GH.purple], stop: ["■", GH.red] };
    const time = (ms) => { const d = new Date(ms); return [d.getHours(), d.getMinutes(), d.getSeconds()].map((n) => String(n).padStart(2, "0")).join(":"); };
    const tagged = pg.watchers.length > 1;
    const activity = card("l-activity", "Activity", undefined, pg.activity.length ? pg.activity.map((a, i) => {
      const [icon, color] = ICONS[a.kind] ?? ["•", t.muted];
      return h_flex().id(`la-${i}`).gap(8).items_start().text_size(11.5)
        .child(div().flex_none().font_family(mono).text_size(10.5).text_color(t.dim).pt(1).child(time(a.at)))
        .child(div().w(12).flex_none().text_color(color).child(icon))
        .when(tagged, (el) => el.child(div().flex_none().max_w(110).truncate().text_color(GH.blue).child(a.board)))
        .child(a.card
          ? h_flex().flex_1().min_w(0).flex_wrap().gap(5).items_center()
            .child(div().font_semibold().child(`#${a.card.number}`))
            .child(this.badge(a.card.color, a.card.status))
            .child(div().min_w(0).child(a.card.title))
            .child(div().text_color(t.dim).child(`· rule ${a.card.rule}`))
          : div().flex_1().min_w(0).text_color(t.muted).child(a.text));
    }) : [div().text_size(11.5).text_color(t.dim).child("Nothing yet.")]);

    return v_flex().id("link-page").flex_1().min_h(0).overflow_y_scrollbar().child(v_flex().p(12).gap(10)
      .child(watchers)
      .when(pg.watchers.length > 0, (el) => el.child(tiles).child(rulesCard))
      .when(pg.watchers.length > 0 && rules.length === 0 && sent === 0, (el) => el.child(connect))
      .child(activity));
  }

  // --- detail -------------------------------------------------------------------------------------
  detail(ui, s, d) {
    const { t } = ui;
    const card = d.card, pr = d.pr;
    const color = pr ? prColor(pr) : d.color || GH.gray;
    const state = pr ? (pr.draft && pr.state === "OPEN" ? "Draft" : pr.state[0] + pr.state.slice(1).toLowerCase())
      : card.state ? card.state[0] + card.state.slice(1).toLowerCase() : "";
    const kind = { ISSUE: "Issue", PULL_REQUEST: "Pull request", DRAFT_ISSUE: "Draft issue", REDACTED: "Private item" }[card.kind] ?? card.kind;
    const rows = [];
    const row = (k, v, c) => { if (v) rows.push([k, v, c]); };
    if (pr) {
      row("Author", `@${pr.author}`);
      row("Branch", `${pr.head} → ${pr.base}`);
      row("Changes", `+${pr.additions} −${pr.deletions} in ${plural(pr.files, "file")}`);
      if (pr.state === "OPEN") {
        row("Review", REVIEW[pr.review]?.[0] ?? "not required", REVIEW[pr.review]?.[1]);
        row("Mergeable", { MERGEABLE: "yes", CONFLICTING: "no: conflicts", UNKNOWN: "checking…" }[pr.mergeable], pr.mergeable === "CONFLICTING" ? GH.red : undefined);
      }
      if (pr.reviews?.length) row("Reviews", pr.reviews.join(", "));
      row("Checks", CHECKS[pr.checks]?.[0], CHECKS[pr.checks]?.[1]);
      if (pr.state === "MERGED") row("Merged", when(pr.merged) + (pr.mergedBy ? ` by @${pr.mergedBy}` : ""));
      if (pr.state === "CLOSED") row("Closed", when(pr.closed));
    }
    if (card.assignees?.length) row("Assignees", "@" + card.assignees.join(", @"));
    if (card.labels?.length) row("Labels", card.labels.join(", "));
    for (const [k, v] of card.fields ?? []) row(k, v);
    if (card.updated) row("Updated", when(card.updated));

    const section = (title) => h_flex().pt(6).gap(8).items_center()
      .child(div().flex_none().font_semibold().text_color(color).child(title))
      .child(div().flex_1().h(1).bg(t.border));
    const bodyW = Math.max(120, (this.detailW ?? 360) - 32);
    const md = (id, text, inset = 0) => this.rich(ui, id, text, d.images, bodyW - inset);
    let description;
    if (card.kind === "REDACTED") description = div().text_color(t.muted).child("You can't see this card's content.");
    else if (d.loading) description = h_flex().gap(6).items_center().text_color(t.muted).child(svg(ICON("spinner")).size(12).spin(900)).child("Loading…");
    else if (d.error) description = div().text_color(GH.red).child(d.error);
    else description = v_flex().gap(8)
      .when(Boolean(d.author), (el) => el.child(div().text_size(11.5).text_color(t.muted).child(`@${d.author} opened ${when(d.created)}`)))
      .child(md("body", d.body));

    return v_flex().flex_1().min_h(0)
      .child(h_flex().flex_none().px(12).py(8).gap(8).items_center().border_b(1).border_color(t.border)
        .child(this.button(ui, "back", "back", "Back", (_e, cx) => { this.confirm = null; this.send(cx, { cmd: "close" }); }))
        .child(div().flex_1())
        .child(this.button(ui, "detail-open", "external", "Open on GitHub", () => this.open(card.url), { disabled: !card.url })))
      .child(v_flex().id("detail-scroll").flex_1().min_h(0).overflow_y_scrollbar()
        .on_size_change((sz, cx) => { if (Math.abs((this.detailW ?? 0) - sz.width) > 1) { this.detailW = sz.width; cx.notify(); } })
        .child(v_flex().px(16).py(14).gap(10)
        .child(div().text_size(16).font_semibold().child(card.title))
        .child(h_flex().flex_wrap().gap(8).items_center().text_size(11.5).text_color(t.muted)
          .when(Boolean(state), (el) => el.child(this.badge(color, state, true)))
          .child(div().child(kind))
          .when(card.kind !== "DRAFT_ISSUE", (el) => el.child(div().font_family(ui.mono).text_size(11).child(card.ref))))
        .when(Boolean(pr) && pr.state === "OPEN", (el) => el.child(this.mergeBox(ui, s, pr)))
        .child(v_flex().gap(4).p(10).rounded(8).bg(t.surface).border(1).border_color(t.border).children(rows.map(([k, v, c], i) =>
          h_flex().id(`f-${i}`).gap(10).items_start().text_size(12)
            .child(div().w(110).flex_none().text_color(t.muted).truncate().child(k))
            .child(div().flex_1().min_w(0).text_color(c ?? t.foreground).child(v)))))
        .child(section("Description"))
        .child(description)
        .when((d.total ?? 0) > 0, (el) => el
          .child(section(d.total > (d.comments?.length ?? 0) ? `Comments (latest ${d.comments.length} of ${d.total})` : `Comments (${d.total})`))
          .children((d.comments ?? []).map((c, i) => v_flex().id(`c-${i}`).gap(6).p(10).rounded(8).bg(t.surface).border(1).border_color(t.border)
            .child(h_flex().gap(6).text_size(11.5).child(div().font_semibold().child(`@${c.author}`)).child(div().text_color(t.dim).child(when(c.created))))
            .child(md(`comment-${i}`, c.body, 22)))))));
  }

  mergeBox(ui, s, pr) {
    const { t } = ui;
    const p = s.prs;
    const merging = p.merging === pr.number;
    const err = p.mergeErrorOf === pr.number ? p.mergeError : null;
    const box = v_flex().gap(8).p(10).rounded(8).bg(tint(GH.purple, 0.07)).border(1).border_color(tint(GH.purple, 0.4));
    if (pr.noMerge) {
      return box.child(h_flex().gap(6).items_center().child(svg(ICON("merge")).size(13).text_color(t.muted)).child(div().text_color(t.muted).child(pr.noMerge)));
    }
    const methods = p.methods ?? [];
    const pick = methods.find((m) => m.id === this.method[pr.number]) ?? methods.find((m) => m.id === "SQUASH") ?? methods[0];
    const warns = [];
    if (pr.mergeable === "CONFLICTING") warns.push(["It has conflicts: GitHub will refuse to merge it.", GH.red]);
    if (pr.review === "CHANGES_REQUESTED") warns.push(["A reviewer asked for changes.", GH.amber]);
    if (pr.review === "REVIEW_REQUIRED") warns.push(["It still needs an approving review.", GH.amber]);
    if (pr.checks === "FAILURE" || pr.checks === "ERROR") warns.push(["Checks are failing.", GH.amber]);
    const confirming = this.confirm?.number === pr.number;
    return box
      .child(h_flex().gap(6).items_center().child(svg(ICON("merge")).size(13).text_color(GH.purple)).child(div().font_semibold().child("Merge")))
      .child(h_flex().flex_wrap().gap(4).children(methods.map((m) => {
        const on = m.id === pick?.id;
        return div().id(`method-${m.id}`).h(24).px(8).rounded(6).flex().items_center().text_size(11.5)
          .bg(on ? tint(GH.purple, 0.2) : t.background).border(1).border_color(on ? tint(GH.purple, 0.6) : t.border)
          .text_color(on ? t.foreground : t.muted)
          .when(!on && !merging, (el) => el.cursor_pointer().hover((st) => st.bg(t.hover)).on_click((_e, cx) => { this.method[pr.number] = m.id; this.confirm = null; cx.notify(); }))
          .child(m.label);
      })))
      .children(warns.map(([w, c], i) => h_flex().id(`warn-${i}`).gap(6).items_center().text_size(11.5).text_color(c).child(div().size(6).rounded(3).bg(c)).child(w)))
      .when(Boolean(err), (el) => el.child(div().text_size(11.5).text_color(GH.red).child(err)))
      .child(h_flex().gap(8).items_center()
        .when(merging, (el) => el.child(h_flex().gap(6).items_center().text_color(t.muted).child(svg(ICON("spinner")).size(12).spin(900)).child("Merging…")))
        .when(!merging && !confirming, (el) => el.child(this.button(ui, "merge", "merge", pick ? pick.label : "Merge",
          (_e, cx) => { this.confirm = { number: pr.number, method: pick.id }; cx.notify(); }, { primary: true, color: GH.purple, disabled: !pick })))
        .when(!merging && confirming, (el) => el
          .child(div().text_size(11.5).child(`Merge #${pr.number} into ${pr.base}?`))
          .child(this.button(ui, "merge-yes", "merge", "Confirm merge", (_e, cx) => {
            this.send(cx, { cmd: "merge", number: pr.number, method: this.confirm.method }); this.confirm = null; cx.notify();
          }, { primary: true, color: GH.purple }))
          .child(this.button(ui, "merge-no", "", "Cancel", (_e, cx) => { this.confirm = null; cx.notify(); }))));
  }

  // --- pickers ------------------------------------------------------------------------------------
  openPage(cx, page) {
    this.page = page;
    this.filter = "";
    this.pick.set_value("");
    this.pick.focus();
    this.send(cx, { cmd: page });
    cx.notify();
  }

  picker(ui, s, forced) {
    const { t } = ui;
    const page = forced ?? this.page;
    const q = this.filter.trim().toLowerCase();
    let title, list, loading, error, rows;
    if (page === "projects") {
      title = "Switch project";
      ({ list = [], loading, error } = s.projects ?? { loading: true });
      rows = list.filter((p) => !q || `${p.title} ${p.owner}/${p.number}`.toLowerCase().includes(q)).map((p, i) => {
        const cur = s.project && p.owner === s.project.owner && p.number === s.project.number;
        return this.pickRow(ui, `proj-${i}`, p.title, `${p.owner} · #${p.number}`, cur, (_e, cx) => {
          if (!cur) this.send(cx, { cmd: "project", owner: p.owner, number: p.number });
          this.page = null; cx.notify();
        });
      });
    } else {
      title = "Pick a repo for pull requests";
      ({ list = [], loading, error } = s.repos ?? { loading: true });
      const typed = /^[\w.-]+\/[\w.-]+$/.test(this.filter.trim()) && !list.some((r) => r.repo.toLowerCase() === q) ? [{ repo: this.filter.trim(), typed: true }] : [];
      rows = [...typed, ...list.filter((r) => !q || r.repo.toLowerCase().includes(q))].map((r, i) => {
        const cur = r.repo === s.prs.repo;
        return this.pickRow(ui, `repo-${i}`, r.repo, r.typed ? "open this repo" : r.project ? "★ in this project" : "", cur, (_e, cx) => {
          this.send(cx, { cmd: "repo", repo: r.repo });
          this.page = null; cx.notify();
        }, r.project ? GH.amber : undefined);
      });
    }
    return v_flex().flex_1().min_h(0)
      .child(h_flex().flex_none().px(12).pt(10).pb(8).gap(8).items_center()
        .when(!forced, (el) => el.child(this.button(ui, "pick-back", "back", "", (_e, cx) => { this.page = null; cx.notify(); }, { tip: "Back" })))
        .child(div().flex_1().font_semibold().child(title))
        .when(Boolean(loading) && list.length > 0, (el) => el.child(svg(ICON("spinner")).size(12).spin(900).text_color(t.muted))))
      .child(h_flex().flex_none().mx(12).mb(8).h(30).px(9).gap(7).items_center().rounded(7).border(1).border_color(t.input).bg(t.surface)
        .child(svg(ICON("search")).size(13).text_color(t.dim).flex_none())
        .child(Input.new(this.pick).flex_1().min_w(0).h_full().text_size(12).text_color(t.foreground)))
      .child(error ? this.empty(ui, error) : !list.length && loading ? this.empty(ui, "Loading…", true)
        : v_flex().id("pick-list").flex_1().min_h(0).px(12).pb(12).gap(4).overflow_y_scrollbar()
          .children(rows.length ? rows : [div().p(12).text_color(t.dim).child("Nothing matches.")]));
  }

  pickRow({ t }, id, title, note, current, action, noteColor) {
    return h_flex().id(id).flex_none().h(36).px(10).gap(8).items_center().rounded(7).cursor_pointer()
      .bg(current ? tint(GH.blue, 0.12) : t.background).border(1).border_color(current ? tint(GH.blue, 0.5) : t.border)
      .hover((st) => st.bg(t.hover)).on_click(action)
      .child(div().flex_1().min_w(0).truncate().font_medium().child(title))
      .when(Boolean(note), (el) => el.child(div().flex_none().text_size(11).text_color(noteColor ?? t.dim).child(note)))
      .when(current, (el) => el.child(this.badge(GH.blue, "current")));
  }
}
