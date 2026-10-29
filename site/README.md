# Unbroke website

A static page (`index.html`) plus `icons.js`, the brand marks of the 35 watched APIs.
No build step, no framework, no tracking. Fonts load from Google Fonts; everything else
is local.

`icons.js` holds paths from [Simple Icons](https://simpleicons.org) v13.21.0 (CC0). Plaid,
Telnyx, LaunchDarkly and Microsoft Graph aren't in that set, so they show navy monogram
tiles. The footer says the marks belong to their owners and imply no endorsement; keep
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
  scan (27 changes, 1 affecting, 50 call sites), `unbroke catalog verify` (35 APIs,
  39,342 endpoints) and `scripts/benchmark.py` (2,433 endpoints, 27 APIs, 1,422 breaking
  changes, and the per-API badges on the coverage wall, which mirror `docs/benchmark.md`).
  Re-run those and update the page if they change.

## Motion

- Hero: headline rises in; "breaks." scatters in coral, then snaps back and gets a mended
  blue underline. On the right, the 27 most-used APIs orbit the Unbroke mark on three
  rings; one at a time an icon fires, and a card shows what Unbroke did with that change
  (Stripe's affects the sample code, the rest are ignored). Gradient blobs drift and lean
  away from the pointer. On phones the orbit becomes a moving strip of icons.
- The product window starts small and grows to fill the screen as you scroll, then plays
  four beats driven by scroll position (watch, filter, pinpoint, mend), with captions.
- The problem statement lights up word by word while API icons drift past at different
  speeds.
- The film: six animated scenes (2:14 AM alert, the changelog line, the filter, the call
  sites, the fix, 9:02 AM merge) that play when in view, pause when scrolled away, and
  have a play button and chapters. It is built in HTML and CSS, so it stays sharp and
  costs nothing to load. To export it as an MP4 for the YC video, screen-record the
  section (or install ffmpeg and capture it headlessly).
- Coverage wall: all 35 APIs with their real six-month breaking-change counts as badges;
  tiles ripple in, and random ones ping as if a change just landed.
- Bento cards have live illustrations (radar of APIs, a funnel where one change gets
  through, languages lighting up, a CI check cycling its outcomes).
- Closing section: every API circles the final call to action.
- A reading-progress line runs along the top. Numbers count up; the CLI types a real scan.
- People who set "reduce motion" get the finished page with no animation.

`preview/` has screenshots of the hero, the scroll story and the phone layout.
