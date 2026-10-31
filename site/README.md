# Unbroke website

A static page (`index.html`) plus `icons.js`, the brand marks and categories of the 122 watched APIs.
No build step, no framework, no tracking. Fonts load from Google Fonts; everything else
is local.

`icons.js` lists every watched API with its mark. 75 use paths from
[Simple Icons](https://simpleicons.org) v13.21.0 (CC0). The other 47 are each company's own
site icon (its apple-touch icon or favicon, or its GitHub organisation picture where the site
had only a tiny one), saved as 128px images in `logos/`. Nothing is redrawn. If a file fails
to load, the tile falls back to initials. Before launch, check each company's brand
guidelines: the marks are trademarks, used here only to say which API Unbroke reads. The
footer says so; keep that line, and remove any logo a company asks you to.

## View it

```bash
cd site && python3 -m http.server 8811   # then open http://127.0.0.1:8811
```

## Before you deploy: do these in order

The page assumes things that are only true once you have done them, so deploy last.

1. **The GitHub repository is public** (`github.com/yugsoni21/unbroke`). Push the latest commits
   first: `go install` only works when the repository's `go.mod` says
   `module github.com/yugsoni21/unbroke`. Add a `LICENSE` file, because without one the code is
   not legally open source, which the page claims.
2. **Set up the signup form on your hosting** (next section).
3. **Read `privacy.html`.** It describes exactly what the form collects and does (email and the
   answers, saved on your own hosting, never sold, kept until someone asks for removal). If you
   change how signups are handled, change that page too, and have someone knowledgeable read it.
4. Upload the whole `site/` folder, including `api/`, `fonts/`, `logos/`, `og.png`,
   `robots.txt` and `sitemap.xml`. Both `unbroke.si` and `www.unbroke.si` serve the page; the
   canonical tag tells search engines which one counts, and a redirect from one to the other in
   your host's settings is better still.

## The signup form on Hostinger

"Get early access" opens a four-step form (email, the APIs you build on, how big your setup is,
which plan) and posts it to `api/signup.php`, a small PHP script that saves each signup on **your
own hosting account**. No form service, no third party. It needs a Hostinger web hosting plan
with PHP 8.0 or newer (the default). A site-builder-only plan has no PHP and cannot run it.

**Set it up (about ten minutes)**

1. In Hostinger's hPanel open **Websites, File Manager, `public_html`** and upload the contents of
   this `site/` folder, keeping the `api/` folder as it is.
2. Check **Advanced, PHP Configuration** shows PHP **8.0 or higher**.
3. In `public_html/api/`, copy `config.example.php` to **`config.php`** and edit it:
   - `notify_email` and `from_email`: to be emailed at each new signup. Create a mailbox like
     `no-reply@unbroke.si` under **Emails** and use that for `from_email`, or the message may be
     treated as spam. Leave both empty for no emails.
   - `admin_password`: a long unique password (12+ characters) turns on the private page
     `https://unbroke.si/api/admin.php` (sign in with `admin_user` and this password), which lists
     signups, shows which APIs people ask for most, and downloads a spreadsheet. It stays off
     until you set one.
   - `data_dir` (recommended): save signups **outside** `public_html`, for example
     `'/home/u123456789/unbroke-data'` (your account folder is shown in File Manager). It is
     created automatically. By default they go in `api/data/`, which is protected, but outside the
     web root is safer.
4. **Test it.** Open the live site, press Get early access and submit with your own email. Then open
   `https://unbroke.si/api/admin.php`, or look for `signups.jsonl` in the data folder.
5. **Check the private files are private.** These addresses must NOT show content, only an error
   (403 or 404): `https://unbroke.si/api/data/signups.jsonl`, `https://unbroke.si/api/config.example.php`.
   If either shows text, tell your host or move `data_dir` outside `public_html`.
6. **Back it up.** Download the spreadsheet from the admin page now and then. The data lives only on
   your hosting account.

`config.php` and the saved signups are in `.gitignore`, so they never go into git. Never put a
password in `config.example.php`.

**What it protects against.** Only JSON posts from your own pages are accepted; a hidden field and a
minimum fill-in time stop most bots (they are told it worked and nothing is saved); one connection can
submit 8 times an hour; every field is length-limited and cleaned before it is saved; the admin page
escapes everything it shows and defuses spreadsheet formulas in the download. A visitor's network
address is never stored with their answers.

**If the form says it can't be reached.** The most common causes: `api/` wasn't uploaded, the PHP version
is below 8.0, or the folder can't be written to (File Manager, right-click `api`, set permissions to
755). Hostinger's PHP error log (hPanel, Advanced) shows the details.

**Using a form service instead.** If you'd rather use Formspree, Tally or your own API, set the
`data-endpoint` attribute on `#joinForm` in `index.html` to its URL. It receives a JSON POST with
`email`, `plan`, `repos`, `needs`, `apis`, `note` and `source`, and must answer with JSON containing
`"ok": true` on success (Formspree does). Then update `privacy.html`.

## Search and sharing

`index.html` carries a canonical address, Open Graph and Twitter tags, and `og.png` (1200x630) as the link-preview
image. `robots.txt` and `sitemap.xml` list the two pages. The site makes no requests to any
other host (the three fonts are hosted in `fonts/`, under the SIL Open Font License), sets no cookies and stores
nothing, which is what `privacy.html` promises. If you add analytics or a form provider's script, update that page.

## Before it goes live

- **Early access form and deploy.** See the two sections above.
- **Numbers.** Every figure on the page comes from the product's own runs: the Stripe
  scan (27 changes, 1 affecting, 50 call sites), `unbroke catalog verify` (122 APIs,
  62,931 endpoints) and `scripts/benchmark.py` (2,547 endpoints, 27 APIs, 1,418 breaking
  changes, and the per-API badges on the coverage wall, which mirror `docs/benchmark.md`).
  Re-run those and update the page if they change.

## Motion

- Hero: headline rises in; "breaks." scatters in coral, then snaps back and gets a mended
  blue underline. On the right, the 27 most-used APIs orbit the Unbroke mark on three
  rings; one at a time an icon fires, and a card shows what Unbroke did with that change
  (Stripe's affects the sample code, the rest are ignored). Gradient blobs drift and lean
  away from the pointer. On phones the orbit becomes a moving strip of icons.
- The product window starts small and grows to fill the screen as you scroll. Inside is
  the real Unbroke app (`unbroke ui`), rebuilt from its own stylesheet and filled with a
  real scan of stripe-samples/accept-a-payment. As you scroll, a pointer types the path
  and presses Scan, the real progress steps tick off, the funnel counts 27 → 1 → 50, the
  pointer picks call sites and the code pane follows (server.go:110, then acss.php:5),
  then it types a test command and presses Create fix branch. The fix result is an
  example, and the window says so while it shows. If the app's design changes, update
  the `u-` styles and the data at the top of the product-window script to match.
- The problem statement lights up word by word while API icons drift past at different
  speeds.
- The film: six animated scenes (2:14 AM alert, the changelog line, the filter, the call
  sites, the fix, 9:02 AM merge) that play when in view, pause when scrolled away, and
  have a play button and chapters. It is built in HTML and CSS, so it stays sharp and
  costs nothing to load. To export it as an MP4 for the YC video, screen-record the
  section (or install ffmpeg and capture it headlessly).
- Coverage wall: all 122 APIs with category filters. The first seven rows show by default and
  "Show all" reveals the rest. The 27 APIs in the benchmark carry their real six-month
  breaking-change counts as badges; the others say "not benchmarked yet". Tiles ripple in
  and random ones ping as if a change just landed.
- Bento cards have live illustrations (radar of APIs, a funnel where one change gets
  through, languages lighting up, a CI check cycling its outcomes).
- Pricing: three plans (Basic $19, Pro $29, Max $69 a month, or two months free yearly) that fly
  in from the sides and straighten up as you scroll to them. A switch rolls the prices between
  monthly and yearly, the Pro card has a spinning colour border, cards tilt toward the pointer,
  and each feature ticks in one by one. A "Find your plan" tool (repositories slider, four
  needs, an engineer-hour rate) picks the plan that fits, lights it up above and says how many
  minutes of saved work pay for it. Choosing a plan carries through to the early-access form
  (the plan name is added to what the form sends). The prices are planned, not live: the page
  says nothing is charged yet. Change them in the `PLAN` table in the script and in the three
  cards' markup, and keep the FAQ answer "What does it cost?" in step.
- Early-access wizard: every "Get early access" button opens a full-screen dialog with four steps
  (email; pick the APIs you build on from the 122, with search and category filters; how many
  repositories and what you need; which plan, plus an optional note). A panel on the left fills in
  live with the APIs you pick, each with its real six-month change count from the benchmark and a
  growing bar, so people see what Unbroke would watch for them. The plan step suggests the same plan
  as the pricing finder, a plan chosen on the pricing cards carries through, and the closing form's
  email carries in. The result is a confetti-and-checkmark screen with the install command. It works
  from the keyboard (Enter continues, Esc closes) and on phones, and shows friendly errors if the
  server can't be reached. It posts to `api/signup.php`; see "The signup form on Hostinger".
- Closing section: every API circles the final call to action.
- A reading-progress line runs along the top. Numbers count up; the CLI types a real scan.
- People who set "reduce motion" get the finished page with no animation.

`preview/` has screenshots of the hero, the scroll story and the phone layout.
