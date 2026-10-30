# Unbroke website

A static page (`index.html`) plus `icons.js`, the brand marks and categories of the 122 watched APIs.
No build step, no framework, no tracking. Fonts load from Google Fonts; everything else
is local.

`icons.js` holds paths from [Simple Icons](https://simpleicons.org) v13.21.0 (CC0). Providers whose
mark isn't in that set (43 of 122, including Plaid, Telnyx and Chargebee) show navy
initials tiles. A brand's own mark is used only when it is that brand's (xAI and LangSmith
show initials, not X's or LangChain's marks). The footer says the marks belong to their owners and imply no endorsement; keep
that line if the logos stay.

## View it

```bash
cd site && python3 -m http.server 8811   # then open http://127.0.0.1:8811
```

## Before it goes live

- **Early access form.** It collects nothing until you connect it. Set `data-endpoint` on
  `#joinForm` to a form service (Formspree, Tally, Basin, or your own API) that accepts a
  JSON POST of `{"email": "..."}`. Until then it tells visitors signups aren't open yet,
  rather than pretending to save their email.
- **Deploy.** Any static host works: drag the `site/` folder into Netlify or Cloudflare
  Pages, or point Vercel at it. Add your domain there.
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
- Closing section: every API circles the final call to action.
- A reading-progress line runs along the top. Numbers count up; the CLI types a real scan.
- People who set "reduce motion" get the finished page with no animation.

`preview/` has screenshots of the hero, the scroll story and the phone layout.
