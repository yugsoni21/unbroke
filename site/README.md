# Unbroke website

A single self-contained page (`index.html`): no build step, no framework, no tracking.
Fonts load from Google Fonts; everything else is inline.

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
  39,342 endpoints) and `scripts/benchmark.py` (2,433 endpoints, 27 APIs). Re-run those
  and update the page if they change.

## Motion

- Hero: headline rises in; the word "breaks." cracks apart and mends with a gold seam;
  hairline cracks in the background glint; a light beam falls toward the product.
- The product window starts small and grows to fill the screen as you scroll, then plays
  four beats driven by scroll position (watch, filter, pinpoint, mend), with captions.
- The problem statement lights up word by word as you read it.
- Sections fade up on entry; numbers count up; the CLI types out a real scan.
- Cards light up under the pointer; gold buttons lean toward it.
- People who set "reduce motion" get the finished page with no animation.

`preview/` has screenshots of the hero, the scroll story and the phone layout.
