#!/usr/bin/env python3
"""Generate Sports Stream Backend project overview PDF."""

from weasyprint import HTML, CSS

html_content = """<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<style>
  @page {
    size: A4;
    margin: 18mm 15mm 18mm 15mm;
    @bottom-right {
      content: "Page " counter(page) " of " counter(pages);
      font-size: 9pt;
      color: #888;
    }
  }
  * { box-sizing: border-box; margin: 0; padding: 0; }
  body {
    font-family: 'Helvetica Neue', Helvetica, Arial, sans-serif;
    font-size: 10pt;
    color: #1e2330;
    line-height: 1.55;
    background: white;
  }

  /* ── Cover page ─────────────────────────────────────── */
  .cover {
    page: cover;
    page-break-after: always;
    display: flex;
    flex-direction: column;
    justify-content: center;
    min-height: 255mm;
    padding: 30mm 20mm;
    background: linear-gradient(160deg, #0f172a 0%, #1e3a5f 55%, #0f3460 100%);
    color: white;
  }
  .cover .badge {
    display: inline-block;
    background: #38bdf8;
    color: #082032;
    font-size: 9pt;
    font-weight: 700;
    letter-spacing: .18em;
    text-transform: uppercase;
    padding: 5pt 11pt;
    border-radius: 20pt;
    margin-bottom: 18pt;
  }
  .cover h1 {
    font-size: 36pt;
    font-weight: 800;
    letter-spacing: -.5pt;
    line-height: 1.08;
    margin-bottom: 12pt;
    color: #e2f4ff;
  }
  .cover .subtitle {
    font-size: 14pt;
    color: #94bfde;
    margin-bottom: 28pt;
    font-weight: 300;
  }
  .cover .meta-table {
    border-top: 1pt solid rgba(255,255,255,.18);
    padding-top: 18pt;
    display: table;
    width: 100%;
  }
  .cover .meta-row { display: table-row; }
  .cover .meta-label {
    display: table-cell;
    color: #7fb3d3;
    font-size: 9pt;
    font-weight: 600;
    letter-spacing: .12em;
    text-transform: uppercase;
    padding: 5pt 16pt 5pt 0;
    vertical-align: top;
    width: 38%;
  }
  .cover .meta-val {
    display: table-cell;
    color: #dde8f2;
    font-size: 10pt;
    padding: 5pt 0;
    vertical-align: top;
  }
  .cover .tagline {
    margin-top: 28pt;
    padding: 14pt 16pt;
    border: 1pt solid rgba(255,255,255,.14);
    border-radius: 8pt;
    background: rgba(255,255,255,.05);
    font-size: 11pt;
    color: #c8dff0;
    font-style: italic;
  }

  /* ── Section pages ──────────────────────────────────── */
  .section-title {
    font-size: 22pt;
    font-weight: 800;
    color: #0f172a;
    border-bottom: 3pt solid #38bdf8;
    padding-bottom: 6pt;
    margin-bottom: 16pt;
    margin-top: 0;
  }
  .section-title span {
    font-size: 11pt;
    font-weight: 400;
    color: #64748b;
    margin-left: 8pt;
  }

  h2 { font-size: 14pt; font-weight: 700; color: #0f172a; margin: 14pt 0 6pt; }
  h3 { font-size: 11pt; font-weight: 700; color: #1e40af; margin: 12pt 0 4pt; }
  p  { margin-bottom: 8pt; }

  .page-break { page-break-before: always; }

  /* ── Cards ──────────────────────────────────────────── */
  .card-grid {
    display: table;
    width: 100%;
    border-spacing: 7pt;
    margin-bottom: 12pt;
  }
  .card-row { display: table-row; }
  .card {
    display: table-cell;
    background: #f8fafc;
    border: 1pt solid #e2e8f0;
    border-radius: 8pt;
    padding: 12pt 14pt;
    vertical-align: top;
    width: 50%;
  }
  .card .card-icon {
    font-size: 18pt;
    margin-bottom: 6pt;
    display: block;
  }
  .card .card-title {
    font-size: 11pt;
    font-weight: 700;
    color: #0f172a;
    margin-bottom: 4pt;
    display: block;
  }
  .card .card-body {
    font-size: 9.5pt;
    color: #475569;
    line-height: 1.5;
  }

  /* ── Service boxes ──────────────────────────────────── */
  .svc-grid {
    display: table;
    width: 100%;
    border-spacing: 5pt;
    margin-bottom: 12pt;
  }
  .svc-row { display: table-row; }
  .svc {
    display: table-cell;
    border-radius: 7pt;
    padding: 10pt 12pt;
    vertical-align: top;
    font-size: 9pt;
    line-height: 1.45;
  }
  .svc-name { font-size: 10.5pt; font-weight: 700; display: block; margin-bottom: 3pt; }
  .svc-port { font-size: 8.5pt; font-weight: 600; opacity: .75; display: block; margin-bottom: 5pt; }
  .svc-desc { color: #334155; font-size: 9pt; }
  .svc.blue  { background: #eff6ff; border: 1pt solid #bfdbfe; }
  .svc.green { background: #f0fdf4; border: 1pt solid #bbf7d0; }
  .svc.purple{ background: #faf5ff; border: 1pt solid #e9d5ff; }
  .svc.amber { background: #fffbeb; border: 1pt solid #fde68a; }
  .svc.rose  { background: #fff1f2; border: 1pt solid #fecdd3; }
  .svc.teal  { background: #f0fdfa; border: 1pt solid #99f6e4; }
  .svc.slate { background: #f8fafc; border: 1pt solid #e2e8f0; }

  /* ── Route table ────────────────────────────────────── */
  table.routes {
    width: 100%;
    border-collapse: collapse;
    font-size: 9pt;
    margin-bottom: 12pt;
  }
  table.routes th {
    background: #0f172a;
    color: #e2e8f0;
    padding: 7pt 9pt;
    text-align: left;
    font-size: 8.5pt;
    letter-spacing: .06em;
    text-transform: uppercase;
  }
  table.routes td {
    padding: 7pt 9pt;
    border-bottom: 1pt solid #e2e8f0;
    vertical-align: top;
  }
  table.routes tr:nth-child(even) td { background: #f8fafc; }
  table.routes .method {
    font-weight: 700;
    font-family: monospace;
    font-size: 8.5pt;
    padding: 2pt 5pt;
    border-radius: 4pt;
    display: inline-block;
  }
  .get    { background: #dcfce7; color: #166534; }
  .post   { background: #dbeafe; color: #1e3a8a; }
  .patch  { background: #fef9c3; color: #713f12; }
  .delete { background: #fee2e2; color: #991b1b; }

  /* ── Inline code ────────────────────────────────────── */
  code {
    font-family: 'Courier New', Courier, monospace;
    font-size: 8.5pt;
    background: #f1f5f9;
    border: 1pt solid #e2e8f0;
    border-radius: 3pt;
    padding: 1pt 4pt;
    color: #0f172a;
  }

  /* ── Code block ─────────────────────────────────────── */
  pre {
    background: #0f172a;
    color: #e2e8f0;
    font-family: 'Courier New', Courier, monospace;
    font-size: 8.5pt;
    border-radius: 7pt;
    padding: 12pt;
    overflow: hidden;
    margin-bottom: 12pt;
    line-height: 1.6;
  }

  /* ── Callout / info box ─────────────────────────────── */
  .callout {
    padding: 10pt 14pt;
    border-radius: 7pt;
    margin-bottom: 12pt;
    font-size: 9.5pt;
  }
  .callout.info  { background: #eff6ff; border-left: 4pt solid #3b82f6; color: #1e3a8a; }
  .callout.warn  { background: #fffbeb; border-left: 4pt solid #f59e0b; color: #78350f; }
  .callout.good  { background: #f0fdf4; border-left: 4pt solid #22c55e; color: #14532d; }

  /* ── Flow diagram text art ──────────────────────────── */
  .arch-box {
    background: #0f172a;
    color: #94d2f8;
    font-family: 'Courier New', Courier, monospace;
    font-size: 8pt;
    border-radius: 7pt;
    padding: 14pt 16pt;
    line-height: 1.8;
    margin-bottom: 14pt;
    white-space: pre;
  }

  /* ── Stats strip ────────────────────────────────────── */
  .stats-strip {
    display: table;
    width: 100%;
    border-spacing: 6pt;
    margin-bottom: 14pt;
  }
  .stats-strip-row { display: table-row; }
  .stat-cell {
    display: table-cell;
    text-align: center;
    padding: 12pt 6pt;
    border-radius: 8pt;
    vertical-align: middle;
  }
  .stat-cell .big { font-size: 24pt; font-weight: 800; display: block; line-height: 1; }
  .stat-cell .lbl { font-size: 8.5pt; font-weight: 600; letter-spacing: .1em; text-transform: uppercase; display: block; margin-top: 4pt; }
  .s1 { background: #eff6ff; color: #1d4ed8; }
  .s2 { background: #f0fdf4; color: #15803d; }
  .s3 { background: #faf5ff; color: #7e22ce; }
  .s4 { background: #fff7ed; color: #c2410c; }
  .s5 { background: #f0fdfa; color: #0f766e; }

  /* ── List styles ────────────────────────────────────── */
  ul, ol { padding-left: 18pt; margin-bottom: 8pt; }
  li { margin-bottom: 3pt; }
  li::marker { color: #38bdf8; }

  /* ── Tag pill ───────────────────────────────────────── */
  .tag {
    display: inline-block;
    font-size: 8pt;
    font-weight: 700;
    letter-spacing: .08em;
    text-transform: uppercase;
    padding: 2pt 7pt;
    border-radius: 12pt;
    margin: 2pt 2pt;
  }
  .tag.go    { background: #dbeafe; color: #1e40af; }
  .tag.gcp   { background: #dcfce7; color: #166534; }
  .tag.fire  { background: #fff7ed; color: #9a3412; }
  .tag.k8s   { background: #faf5ff; color: #6b21a8; }
  .tag.pub   { background: #fef9c3; color: #713f12; }
  .tag.hls   { background: #ffe4e6; color: #9f1239; }
  .tag.fcm   { background: #f0fdfa; color: #134e4a; }

  /* ── Divider ────────────────────────────────────────── */
  hr { border: none; border-top: 1pt solid #e2e8f0; margin: 14pt 0; }

  /* ── TOC ────────────────────────────────────────────── */
  .toc-entry {
    display: table;
    width: 100%;
    padding: 5pt 0;
    border-bottom: 1pt dotted #e2e8f0;
  }
  .toc-left { display: table-cell; font-size: 10.5pt; color: #0f172a; }
  .toc-page { display: table-cell; text-align: right; font-size: 9.5pt; color: #64748b; }
  .toc-section { font-weight: 700; font-size: 11pt; color: #1e40af; }
  .toc-sub    { padding-left: 14pt; font-size: 9.5pt; color: #334155; }
</style>
</head>
<body>

<!-- ══════════════════════════════════════════════════════ COVER ══ -->
<div class="cover">
  <div class="badge">Technical Overview — May 2026</div>
  <h1>Sports Stream<br>Backend Platform</h1>
  <p class="subtitle">Scalable live sports streaming infrastructure built on Go microservices &amp; Google Cloud Platform</p>

  <div class="meta-table">
    <div class="meta-row">
      <div class="meta-label">Project Name</div>
      <div class="meta-val">sports-stream-backend</div>
    </div>
    <div class="meta-row">
      <div class="meta-label">Language / Runtime</div>
      <div class="meta-val">Go 1.25 — compiled to static binaries</div>
    </div>
    <div class="meta-row">
      <div class="meta-label">Cloud Platform</div>
      <div class="meta-val">Google Cloud Platform (GCP) — Cloud Run / Kubernetes</div>
    </div>
    <div class="meta-row">
      <div class="meta-label">Database</div>
      <div class="meta-val">Cloud Firestore (NoSQL, real-time)</div>
    </div>
    <div class="meta-row">
      <div class="meta-label">Authentication</div>
      <div class="meta-val">Firebase Auth — JWT / Google Sign-In</div>
    </div>
    <div class="meta-row">
      <div class="meta-label">Client App</div>
      <div class="meta-val">Android (ExoPlayer HLS streaming)</div>
    </div>
    <div class="meta-row">
      <div class="meta-label">Architecture Style</div>
      <div class="meta-val">Microservices — Gateway + 5 independent services</div>
    </div>
    <div class="meta-row">
      <div class="meta-label">Deployment</div>
      <div class="meta-val">Docker container — single image, Cloud Run / K8s</div>
    </div>
  </div>

  <div class="tagline">
    "A production-grade live sports streaming platform — from RTMP ingest to HLS delivery, real-time viewer tracking, push notifications, and a full admin control panel."
  </div>
</div>


<!-- ══════════════════════════════════════════════════════ TOC ══ -->
<h1 class="section-title">Table of Contents</h1>

<div class="toc-entry"><div class="toc-left toc-section">1 &nbsp; Project Overview</div><div class="toc-page">3</div></div>
<div class="toc-entry"><div class="toc-left toc-sub">1.1 &nbsp; What is Sports Stream?</div><div class="toc-page">3</div></div>
<div class="toc-entry"><div class="toc-left toc-sub">1.2 &nbsp; Key Numbers at a Glance</div><div class="toc-page">3</div></div>
<div class="toc-entry"><div class="toc-left toc-sub">1.3 &nbsp; Technology Stack</div><div class="toc-page">3</div></div>
<div class="toc-entry"><div class="toc-left toc-section">2 &nbsp; System Architecture</div><div class="toc-page">4</div></div>
<div class="toc-entry"><div class="toc-left toc-sub">2.1 &nbsp; Architecture Diagram</div><div class="toc-page">4</div></div>
<div class="toc-entry"><div class="toc-left toc-sub">2.2 &nbsp; Service Responsibilities</div><div class="toc-page">4</div></div>
<div class="toc-entry"><div class="toc-left toc-sub">2.3 &nbsp; Event-Driven Flow</div><div class="toc-page">5</div></div>
<div class="toc-entry"><div class="toc-left toc-section">3 &nbsp; Microservices Deep Dive</div><div class="toc-page">5</div></div>
<div class="toc-entry"><div class="toc-left toc-sub">3.1 &nbsp; API Gateway (port 8080)</div><div class="toc-page">5</div></div>
<div class="toc-entry"><div class="toc-left toc-sub">3.2 &nbsp; User Service (port 8081)</div><div class="toc-page">6</div></div>
<div class="toc-entry"><div class="toc-left toc-sub">3.3 &nbsp; Stream Service (port 8082)</div><div class="toc-page">6</div></div>
<div class="toc-entry"><div class="toc-left toc-sub">3.4 &nbsp; Notification Service (port 8083)</div><div class="toc-page">7</div></div>
<div class="toc-entry"><div class="toc-left toc-sub">3.5 &nbsp; Admin Service (port 8084)</div><div class="toc-page">7</div></div>
<div class="toc-entry"><div class="toc-left toc-sub">3.6 &nbsp; Analytics Service (port 8085)</div><div class="toc-page">8</div></div>
<div class="toc-entry"><div class="toc-left toc-section">4 &nbsp; API Reference</div><div class="toc-page">9</div></div>
<div class="toc-entry"><div class="toc-left toc-section">5 &nbsp; Data Model</div><div class="toc-page">10</div></div>
<div class="toc-entry"><div class="toc-left toc-section">6 &nbsp; Video Pipeline</div><div class="toc-page">11</div></div>
<div class="toc-entry"><div class="toc-left toc-section">7 &nbsp; Security &amp; Authentication</div><div class="toc-page">11</div></div>
<div class="toc-entry"><div class="toc-left toc-section">8 &nbsp; Infrastructure &amp; Deployment</div><div class="toc-page">12</div></div>
<div class="toc-entry"><div class="toc-left toc-section">9 &nbsp; Observability &amp; Health</div><div class="toc-page">12</div></div>
<div class="toc-entry"><div class="toc-left toc-section">10  Future Roadmap (AWS Migration)</div><div class="toc-page">13</div></div>


<!-- ══════════════════════════════════════════════════ SECTION 1 ══ -->
<div class="page-break"></div>
<h1 class="section-title">1 &nbsp; Project Overview <span>What is Sports Stream?</span></h1>

<h2>1.1 &nbsp; What is Sports Stream?</h2>
<p>
  Sports Stream is a <strong>live sports streaming backend platform</strong> that powers a mobile Android application. It allows sports broadcasters to stream live matches via RTMP, which the platform transcodes into HLS (HTTP Live Streaming) format for delivery to viewers. The system handles everything from user authentication, real-time viewer tracking, push notifications, analytics collection, to full admin management.
</p>
<p>
  The project was built for the <strong>CCC'26 competition</strong> and demonstrates a production-ready microservices architecture deployed on Google Cloud Platform.
</p>

<h2>1.2 &nbsp; Key Numbers at a Glance</h2>
<div class="stats-strip">
  <div class="stats-strip-row">
    <div class="stat-cell s1"><span class="big">6</span><span class="lbl">Services</span></div>
    <div class="stat-cell s2"><span class="big">5</span><span class="lbl">GCP Products</span></div>
    <div class="stat-cell s3"><span class="big">30+</span><span class="lbl">API Endpoints</span></div>
    <div class="stat-cell s4"><span class="big">3</span><span class="lbl">Video Qualities</span></div>
    <div class="stat-cell s5"><span class="big">3</span><span class="lbl">User Roles</span></div>
  </div>
</div>

<h2>1.3 &nbsp; Technology Stack</h2>
<p>
  <span class="tag go">Go 1.25</span>
  <span class="tag gcp">Cloud Run</span>
  <span class="tag gcp">Firestore</span>
  <span class="tag gcp">Pub/Sub</span>
  <span class="tag gcp">Transcoder API</span>
  <span class="tag gcp">Cloud Storage</span>
  <span class="tag fire">Firebase Auth</span>
  <span class="tag fcm">Firebase FCM</span>
  <span class="tag k8s">Kubernetes / K8s</span>
  <span class="tag hls">HLS Streaming</span>
  <span class="tag hls">RTMP Ingest</span>
  <span class="tag pub">gorilla/mux</span>
  <span class="tag go">Docker</span>
  <span class="tag gcp">Cloud Build</span>
  <span class="tag gcp">Terraform</span>
</p>

<div class="callout info">
  <strong>Architecture Philosophy:</strong> Each service is independently compiled to a static binary, packaged together in a single Docker image, and started by a <code>start.sh</code> supervisor script. This "monorepo, multi-binary" approach gives microservice isolation while simplifying Cloud Run deployment to a single container.
</div>


<!-- ══════════════════════════════════════════════════ SECTION 2 ══ -->
<div class="page-break"></div>
<h1 class="section-title">2 &nbsp; System Architecture</h1>

<h2>2.1 &nbsp; Architecture Diagram</h2>

<div class="arch-box">
┌─────────────────────────────────────────────────────────────────────────────┐
│                        SPORTS STREAM PLATFORM                               │
│                                                                             │
│  ┌──────────────┐     HTTPS      ┌───────────────────────────────────────┐ │
│  │  Android App │ ──────────────▶│          API Gateway  :8080           │ │
│  │  (ExoPlayer) │                │       (Reverse Proxy / Router)        │ │
│  └──────────────┘                └───────┬───────┬───────┬───────┬───────┘ │
│                                          │       │       │       │         │
│   /api/v1/auth, /users  ─────────────────┘       │       │       │         │
│   /api/v1/streams  ──────────────────────────────┘       │       │         │
│   /api/v1/notifications, /api/v1/analytics  ─────────────┘       │         │
│   /admin, /api/v1/admin  ────────────────────────────────────────┘         │
│                                                                             │
│  ┌─────────────┐  ┌──────────────┐  ┌──────────────┐  ┌──────────────┐   │
│  │ user-svc    │  │ stream-svc   │  │ notif-svc    │  │ admin-svc    │   │
│  │  :8081      │  │   :8082      │  │   :8083      │  │   :8084      │   │
│  └──────┬──────┘  └──────┬───────┘  └──────┬───────┘  └──────┬───────┘   │
│         │                │                 │                  │            │
│         ▼                ▼                 ▼                  ▼            │
│  ┌─────────────────────────────────────────────────────────────────────┐   │
│  │                 Google Cloud Firestore (NoSQL)                      │   │
│  │    users │ streams │ streams/viewers │ analytics │ matches          │   │
│  └─────────────────────────────────────────────────────────────────────┘   │
│                                                                             │
│  stream-svc ──▶ Pub/Sub ──▶ analytics-svc :8085                           │
│             └──▶ Pub/Sub ──▶ notif-svc ──▶ Firebase FCM ──▶ Android App  │
│                                                                             │
│  stream-svc ──▶ GCP Transcoder API ──▶ Cloud Storage (HLS segments)       │
│  CDN Base URL ──▶ HLS .m3u8 ──▶ Android ExoPlayer                        │
└─────────────────────────────────────────────────────────────────────────────┘
</div>

<h2>2.2 &nbsp; Service Responsibilities</h2>

<div class="svc-grid">
  <div class="svc-row">
    <div class="svc blue">
      <span class="svc-name">API Gateway</span>
      <span class="svc-port">Port 8080 — Public entry point</span>
      <span class="svc-desc">Routes all incoming requests to the appropriate microservice using prefix-based path matching. Injects request IDs, serves the landing page and health dashboard. The only service exposed externally.</span>
    </div>
    <div class="svc green">
      <span class="svc-name">User Service</span>
      <span class="svc-port">Port 8081 — Identity &amp; Profiles</span>
      <span class="svc-desc">Handles Firebase JWT verification on login, upserts user profiles in Firestore, serves profile read/update endpoints. New users are assigned the <code>viewer</code> role by default.</span>
    </div>
  </div>
  <div class="svc-row">
    <div class="svc purple">
      <span class="svc-name">Stream Service</span>
      <span class="svc-port">Port 8082 — Live stream management</span>
      <span class="svc-desc">Creates/lists streams, manages viewer join/leave using Firestore transactions with a subcollection for idempotent presence tracking. Triggers GCP Transcoder API for RTMP→HLS. Publishes events to Pub/Sub.</span>
    </div>
    <div class="svc amber">
      <span class="svc-name">Notification Service</span>
      <span class="svc-port">Port 8083 — Push notifications</span>
      <span class="svc-desc">Subscribes to <code>stream_events</code> Pub/Sub topic. Sends Firebase FCM push notifications to the <code>sports_live</code> and <code>match_reminders</code> topics when streams start, end, or matches approach.</span>
    </div>
  </div>
  <div class="svc-row">
    <div class="svc rose">
      <span class="svc-name">Admin Service</span>
      <span class="svc-port">Port 8084 — Operations control</span>
      <span class="svc-desc">Full CRUD for users, streams, matches, and analytics. Web-based admin panel with HMAC session cookies. Real-time analytics via Server-Sent Events (SSE). Serves the embedded SPA admin UI.</span>
    </div>
    <div class="svc teal">
      <span class="svc-name">Analytics Service</span>
      <span class="svc-port">Port 8085 — Viewer metrics</span>
      <span class="svc-desc">Subscribes to <code>viewer_events</code> Pub/Sub. Tracks currentViewers, peakViewers, totalJoins per stream using idempotent Firestore transactions with SHA-256 event fingerprinting to prevent duplicate processing.</span>
    </div>
  </div>
</div>

<h2>2.3 &nbsp; Event-Driven Flow</h2>
<p>The platform uses <strong>Google Cloud Pub/Sub</strong> for asynchronous communication between services:</p>

<div class="card-grid">
  <div class="card-row">
    <div class="card">
      <span class="card-title">stream_events topic</span>
      <span class="card-body">Published by stream-service when a stream starts or ends.<br><br><strong>Subscribers:</strong> notification-service (sends FCM push), analytics-service (initializes/resets analytics doc)</span>
    </div>
    <div class="card">
      <span class="card-title">viewer_events topic</span>
      <span class="card-body">Published by stream-service on every viewer join or leave action.<br><br><strong>Subscribers:</strong> analytics-service (increments/decrements currentViewers, peakViewers, totalJoins)</span>
    </div>
  </div>
</div>


<!-- ══════════════════════════════════════════════════ SECTION 3 ══ -->
<div class="page-break"></div>
<h1 class="section-title">3 &nbsp; Microservices Deep Dive</h1>

<h2>3.1 &nbsp; API Gateway (port 8080)</h2>
<p>The gateway is a <strong>pure HTTP reverse proxy</strong> built with Go's standard <code>net/http/httputil</code> package. It has no authentication logic — all auth happens in downstream services.</p>

<table class="routes">
  <tr><th>Path Pattern</th><th>Forwards To</th><th>Notes</th></tr>
  <tr><td><code>/</code></td><td>Landing page (inline HTML)</td><td>Shows live matches from /api/v1/streams</td></tr>
  <tr><td><code>/health</code></td><td>Internal health check</td><td>Probes all 5 services, renders HTML dashboard</td></tr>
  <tr><td><code>/api/v1/auth/*</code></td><td>user-service :8081</td><td>Token verification</td></tr>
  <tr><td><code>/api/v1/users/*</code></td><td>user-service :8081</td><td>Profile management</td></tr>
  <tr><td><code>/api/v1/streams/*</code></td><td>stream-service :8082</td><td>Stream CRUD &amp; viewer tracking</td></tr>
  <tr><td><code>/api/v1/notifications/*</code></td><td>notification-service :8083</td><td>Test notification trigger</td></tr>
  <tr><td><code>/api/v1/analytics/*</code></td><td>analytics-service :8085</td><td>Viewer statistics</td></tr>
  <tr><td><code>/admin/*</code> / <code>/api/v1/admin/*</code></td><td>admin-service :8084</td><td>Admin panel &amp; APIs</td></tr>
</table>

<h2>3.2 &nbsp; User Service (port 8081)</h2>
<p>Handles the complete <strong>identity lifecycle</strong> for the Android app users.</p>
<ul>
  <li><strong>POST /api/v1/auth/verify</strong> — Accepts a Firebase ID token (from Android Google Sign-In), verifies it with Firebase Admin SDK, then upserts the user profile in Firestore.</li>
  <li><strong>GET /api/v1/users/me</strong> — Returns the authenticated user's full profile including role.</li>
  <li><strong>PATCH /api/v1/users/me</strong> — Updates <code>displayName</code>, <code>favTeams</code>, and <code>fcmToken</code>.</li>
</ul>
<p><strong>User Roles:</strong> <code>viewer</code> (default), <code>broadcaster</code> (can create streams), <code>admin</code> (full access).</p>

<div class="callout good">
  <strong>Role preservation:</strong> When an existing user logs in again, the service only updates email, displayName, and photoUrl — it never overwrites the role field. Promotions are done by admins via the admin panel or Firebase Console.
</div>

<h2>3.3 &nbsp; Stream Service (port 8082)</h2>
<p>The core service managing <strong>live stream lifecycle and viewer presence</strong>.</p>
<ul>
  <li><strong>GET /api/v1/streams</strong> — Lists all <code>status: live</code> streams (max 50), merges <code>currentViewers</code> from analytics.</li>
  <li><strong>POST /api/v1/streams</strong> — Creates a stream (broadcaster/admin only), triggers GCP Transcoder async.</li>
  <li><strong>POST /api/v1/streams/{id}/join</strong> — Atomically increments viewerCount using a Firestore transaction. Idempotent — joining twice returns the current count without double-counting.</li>
  <li><strong>POST /api/v1/streams/{id}/leave</strong> — Atomically decrements viewerCount, floored at 0. Safe across all Cloud Run instances.</li>
  <li><strong>GET /api/v1/streams/{id}/manifest</strong> — Returns the HLS manifest URL for ExoPlayer.</li>
</ul>

<div class="callout warn">
  <strong>Viewer Idempotency Pattern:</strong> A Firestore subcollection <code>streams/{id}/viewers/{uid}</code> stores presence docs. Join/leave transactions first check this subcollection — if a user is already marked present, the viewerCount is not incremented again. This eliminates double-counting across network retries and multiple Cloud Run instances.
</div>

<h2>3.4 &nbsp; Notification Service (port 8083)</h2>
<p>An <strong>event-driven FCM push notification dispatcher</strong>. It runs both an HTTP server (for health checks and test triggers) and a Pub/Sub subscriber goroutine simultaneously.</p>
<ul>
  <li>Subscribes to <code>stream_events</code> Pub/Sub topic.</li>
  <li><code>stream_started</code> → sends "Match is LIVE!" to <code>sports_live</code> FCM topic.</li>
  <li><code>stream_ended</code> → sends "Match Ended" to <code>sports_live</code> FCM topic.</li>
  <li><code>match_reminder</code> → sends "Match starting soon!" to <code>match_reminders</code> FCM topic.</li>
  <li>On Pub/Sub errors, the subscriber retries with a 5-second backoff — the HTTP server stays up.</li>
</ul>
<p><strong>POST /api/v1/notifications/test</strong> — Allows testing notifications without Pub/Sub (direct API call).</p>

<h2>3.5 &nbsp; Admin Service (port 8084)</h2>
<p>A full <strong>operations control center</strong> serving both a REST JSON API and a single-page web application built into the binary itself (no external CDN dependencies).</p>

<div class="card-grid">
  <div class="card-row">
    <div class="card">
      <span class="card-title">Admin Panel Features</span>
      <span class="card-body">
        • Dashboard with live stats (users, streams, viewers)<br>
        • User management — list, create, update role, delete<br>
        • Stream management — list, create, end, reset viewers<br>
        • Match schedule management (CRUD)<br>
        • Analytics leaderboard with viewer metrics<br>
        • Real-time analytics via SSE (no polling)<br>
        • Send push notifications directly<br>
        • System health check for all services
      </span>
    </div>
    <div class="card">
      <span class="card-title">Authentication</span>
      <span class="card-body">
        The admin panel supports two auth methods:<br><br>
        <strong>1. Session cookie (web UI):</strong> HMAC-SHA256 signed token with username/password login. 12-hour expiry. HttpOnly, SameSite=Lax.<br><br>
        <strong>2. Firebase Bearer token (API):</strong> Checks the caller has <code>role: admin</code> in Firestore.
      </span>
    </div>
  </div>
</div>

<h2>3.6 &nbsp; Analytics Service (port 8085)</h2>
<p>Processes viewer and stream events from Pub/Sub to maintain <strong>real-time analytics aggregates</strong> in Firestore.</p>

<div class="card-grid">
  <div class="card-row">
    <div class="card">
      <span class="card-title">Metrics Tracked per Stream</span>
      <span class="card-body">
        • <strong>currentViewers</strong> — live count, incremented/decremented per join/leave event<br>
        • <strong>peakViewers</strong> — maximum concurrent viewers ever recorded<br>
        • <strong>totalJoins</strong> — cumulative join count<br>
        • <strong>viewerHistory</strong> — subcollection with per-user join/leave timestamps and watch count
      </span>
    </div>
    <div class="card">
      <span class="card-title">Idempotency Mechanism</span>
      <span class="card-body">
        Each event has an <code>eventId</code>. A <code>processedEvents</code> subcollection stores processed event IDs. All counter updates run inside Firestore transactions that first check if the event was already processed.<br><br>
        If no eventId is provided, one is generated as a SHA-256 fingerprint of <code>eventType + streamId + uid + timestamp</code>.
      </span>
    </div>
  </div>
</div>


<!-- ══════════════════════════════════════════════════ SECTION 4 ══ -->
<div class="page-break"></div>
<h1 class="section-title">4 &nbsp; API Reference</h1>

<h2>Authentication Endpoints</h2>
<table class="routes">
  <tr><th style="width:8%">Method</th><th style="width:30%">Path</th><th style="width:12%">Auth</th><th>Description</th></tr>
  <tr>
    <td><span class="method post">POST</span></td>
    <td><code>/api/v1/auth/verify</code></td>
    <td>None</td>
    <td>Verify Firebase ID token. Body: <code>{"idToken": "..."}</code>. Returns full user profile with role.</td>
  </tr>
  <tr>
    <td><span class="method get">GET</span></td>
    <td><code>/api/v1/users/me</code></td>
    <td>Bearer</td>
    <td>Get authenticated user profile (uid, email, displayName, favTeams, role)</td>
  </tr>
  <tr>
    <td><span class="method patch">PATCH</span></td>
    <td><code>/api/v1/users/me</code></td>
    <td>Bearer</td>
    <td>Update displayName, favTeams, or fcmToken (role cannot be changed by user)</td>
  </tr>
</table>

<h2>Stream Endpoints</h2>
<table class="routes">
  <tr><th style="width:8%">Method</th><th style="width:35%">Path</th><th style="width:12%">Auth</th><th>Description</th></tr>
  <tr>
    <td><span class="method get">GET</span></td>
    <td><code>/api/v1/streams</code></td>
    <td>None</td>
    <td>List all live streams. Returns array with viewerCount + currentViewers (merged from analytics).</td>
  </tr>
  <tr>
    <td><span class="method post">POST</span></td>
    <td><code>/api/v1/streams</code></td>
    <td>Bearer (broadcaster/admin)</td>
    <td>Create a new stream. Body: <code>{"title":"...", "rtmpUrl":"..."}</code>. Triggers transcoder.</td>
  </tr>
  <tr>
    <td><span class="method get">GET</span></td>
    <td><code>/api/v1/streams/{id}</code></td>
    <td>None</td>
    <td>Get single stream details including merged currentViewers.</td>
  </tr>
  <tr>
    <td><span class="method post">POST</span></td>
    <td><code>/api/v1/streams/{id}/join</code></td>
    <td>Bearer</td>
    <td>Join a stream. Idempotent — returns current viewerCount. Returns 410 Gone if stream ended.</td>
  </tr>
  <tr>
    <td><span class="method post">POST</span></td>
    <td><code>/api/v1/streams/{id}/leave</code></td>
    <td>Bearer</td>
    <td>Leave a stream. Idempotent — no effect if not currently joined. Count never goes below 0.</td>
  </tr>
  <tr>
    <td><span class="method get">GET</span></td>
    <td><code>/api/v1/streams/{id}/manifest</code></td>
    <td>Bearer</td>
    <td>Returns HLS manifest URL for ExoPlayer. Returns 202 Accepted if transcoder not ready yet.</td>
  </tr>
  <tr>
    <td><span class="method post">POST</span></td>
    <td><code>/api/v1/streams/{id}/reset-viewers</code></td>
    <td>Bearer (admin)</td>
    <td>Reset viewerCount to 0 and batch-delete all viewer presence docs.</td>
  </tr>
</table>

<h2>Analytics Endpoints</h2>
<table class="routes">
  <tr><th style="width:8%">Method</th><th style="width:35%">Path</th><th style="width:12%">Auth</th><th>Description</th></tr>
  <tr>
    <td><span class="method get">GET</span></td>
    <td><code>/api/v1/analytics/stream/{id}</code></td>
    <td>None</td>
    <td>Get analytics doc: currentViewers, peakViewers, totalJoins</td>
  </tr>
  <tr>
    <td><span class="method get">GET</span></td>
    <td><code>/api/v1/analytics/stream/{id}/viewers</code></td>
    <td>None</td>
    <td>Get full viewer history list ordered by join time</td>
  </tr>
</table>

<h2>Admin API Endpoints (selected)</h2>
<table class="routes">
  <tr><th style="width:8%">Method</th><th style="width:35%">Path</th><th style="width:12%">Auth</th><th>Description</th></tr>
  <tr><td><span class="method get">GET</span></td><td><code>/api/v1/admin/dashboard</code></td><td>Admin</td><td>Summary stats: users, roles, live streams, total joins</td></tr>
  <tr><td><span class="method get">GET</span></td><td><code>/api/v1/admin/users</code></td><td>Admin</td><td>List all users with role breakdown</td></tr>
  <tr><td><span class="method patch">PATCH</span></td><td><code>/api/v1/admin/users/{uid}/role</code></td><td>Admin</td><td>Promote/demote user role</td></tr>
  <tr><td><span class="method get">GET</span></td><td><code>/api/v1/admin/streams</code></td><td>Admin</td><td>List all streams (including ended)</td></tr>
  <tr><td><span class="method post">POST</span></td><td><code>/api/v1/admin/streams/{id}/end</code></td><td>Admin</td><td>Force-end a live stream</td></tr>
  <tr><td><span class="method get">GET</span></td><td><code>/api/v1/admin/analytics/top</code></td><td>Admin</td><td>Top streams by peak viewers</td></tr>
  <tr><td><span class="method get">GET</span></td><td><code>/api/v1/admin/analytics/sse</code></td><td>Admin</td><td>Server-Sent Events stream for live analytics updates</td></tr>
  <tr><td><span class="method get">GET</span></td><td><code>/api/v1/admin/matches</code></td><td>Admin</td><td>List scheduled matches</td></tr>
  <tr><td><span class="method post">POST</span></td><td><code>/api/v1/admin/notifications/send</code></td><td>Admin</td><td>Send push notification (stream_started / match_reminder)</td></tr>
  <tr><td><span class="method get">GET</span></td><td><code>/api/v1/admin/health</code></td><td>Admin</td><td>Health status of all 6 services</td></tr>
</table>


<!-- ══════════════════════════════════════════════════ SECTION 5 ══ -->
<div class="page-break"></div>
<h1 class="section-title">5 &nbsp; Data Model</h1>
<p>All data is stored in <strong>Cloud Firestore</strong> — a schemaless, real-time NoSQL document database. The platform uses the following top-level collections:</p>

<div class="card-grid">
  <div class="card-row">
    <div class="card">
      <span class="card-title">Collection: users</span>
      <span class="card-body">
        <code>uid</code> (string, PK)<br>
        <code>email</code> (string)<br>
        <code>displayName</code> (string)<br>
        <code>photoUrl</code> (string)<br>
        <code>favTeams</code> (string[])<br>
        <code>role</code> (viewer | broadcaster | admin)<br>
        <code>fcmToken</code> (string, optional)<br>
        <code>createdAt</code>, <code>updatedAt</code> (timestamp)
      </span>
    </div>
    <div class="card">
      <span class="card-title">Collection: streams</span>
      <span class="card-body">
        <code>id</code> (string, e.g. "stream_1714000000000")<br>
        <code>title</code> (string)<br>
        <code>status</code> (live | ended)<br>
        <code>hlsUrl</code> (string — GCS path to m3u8)<br>
        <code>viewerCount</code> (int — managed by transactions)<br>
        <code>broadcasterUid</code> (string, FK → users)<br>
        <code>createdAt</code>, <code>updatedAt</code> (timestamp)<br><br>
        <strong>Subcollection:</strong> <code>viewers/{uid}</code> — presence docs (uid, joinedAt)
      </span>
    </div>
  </div>
  <div class="card-row">
    <div class="card">
      <span class="card-title">Collection: analytics</span>
      <span class="card-body">
        <code>streamId</code> (string, same as stream ID)<br>
        <code>currentViewers</code> (int64)<br>
        <code>peakViewers</code> (int64)<br>
        <code>totalJoins</code> (int64)<br>
        <code>updatedAt</code> (timestamp)<br><br>
        <strong>Subcollections:</strong><br>
        <code>processedEvents/{eventId}</code> — idempotency log<br>
        <code>viewerHistory/{uid}</code> — per-user watch history (joinedAt, leftAt, joinCount, isWatching)
      </span>
    </div>
    <div class="card">
      <span class="card-title">Collection: matches</span>
      <span class="card-body">
        <code>id</code> (string, e.g. "match_1714000000000")<br>
        <code>title</code> (string)<br>
        <code>scheduledAt</code> (timestamp)<br>
        <code>status</code> (scheduled | live | ended)<br>
        <code>createdAt</code>, <code>updatedAt</code> (timestamp)<br><br>
        Used for match schedule management in admin panel and reminder notifications.
      </span>
    </div>
  </div>
</div>


<!-- ══════════════════════════════════════════════════ SECTION 6 ══ -->
<div class="page-break"></div>
<h1 class="section-title">6 &nbsp; Video Pipeline</h1>

<p>The platform uses the <strong>GCP Cloud Transcoder API</strong> for fully managed video encoding — no self-managed FFmpeg servers required.</p>

<div class="arch-box">
Broadcaster ──RTMP──▶  GCP Cloud Transcoder API
                                 │
                    ┌────────────▼─────────────────────┐
                    │     Transcoding Job (async)       │
                    │                                   │
                    │   Input: RTMP URL                 │
                    │   Output: gs://bucket/hls/{id}/   │
                    │                                   │
                    │   Qualities produced:             │
                    │   ┌──────────┬───────────────┐   │
                    │   │ 720p     │ 1.5 Mbps H.264│   │
                    │   │ 480p     │ 800 Kbps H.264│   │
                    │   │ 360p     │ 400 Kbps H.264│   │
                    │   │ Audio    │ 128 Kbps AAC  │   │
                    │   └──────────┴───────────────┘   │
                    │   Segment duration: 2 seconds     │
                    │   Manifest: index.m3u8 (HLS)      │
                    └────────────┬─────────────────────┘
                                 │
                    Cloud Storage (GCS Bucket)
                    gs://sports-stream-66553.appspot.com/hls/{id}/
                                 │
                    CDN Base URL → https://storage.googleapis.com/...
                                 │
                    Android ExoPlayer ◀── index.m3u8
</div>

<p><strong>Flow:</strong> On stream creation, stream-service immediately writes the expected HLS URL to Firestore (so ExoPlayer can start retrying), then polls the transcoder job every 5 seconds for up to 10 minutes. When the job succeeds or fails, the stream is marked as <code>ended</code> and the viewer subcollection is batch-deleted.</p>


<!-- ══════════════════════════════════════════════════ SECTION 7 ══ -->
<h1 class="section-title">7 &nbsp; Security &amp; Authentication</h1>

<div class="card-grid">
  <div class="card-row">
    <div class="card">
      <span class="card-title">Firebase JWT Authentication</span>
      <span class="card-body">
        The Android app authenticates with Google Sign-In and receives a Firebase ID token (JWT). Every protected API call includes this token as an <code>Authorization: Bearer &lt;token&gt;</code> header.<br><br>
        The <code>pkg/middleware</code> package verifies the token using Firebase Admin SDK and injects the verified UID into the request context. Invalid or expired tokens return HTTP 401.
      </span>
    </div>
    <div class="card">
      <span class="card-title">Admin Panel Authentication</span>
      <span class="card-body">
        The admin web UI uses HMAC-SHA256 signed session cookies:<br><br>
        Token format: <code>base64(username|expiry|hmac_sig)</code><br>
        • 12-hour session expiry<br>
        • HttpOnly + SameSite=Lax cookies<br>
        • Separate from Firebase auth<br><br>
        Admin API calls can also use Firebase tokens where the user's Firestore <code>role</code> is <code>admin</code>.
      </span>
    </div>
  </div>
</div>

<div class="callout warn">
  <strong>Role-Based Access Control:</strong> Roles are stored in Firestore and checked server-side on every request. A <code>viewer</code> cannot create streams. A <code>broadcaster</code> can only create streams but not access admin APIs. The <code>admin</code> role has full access to all endpoints. Roles can only be changed by admins via the admin panel.
</div>

<p>Firestore Security Rules are also configured (<code>firestore.rules</code>) to enforce read/write access policies at the database level as a defense-in-depth measure.</p>


<!-- ══════════════════════════════════════════════════ SECTION 8 ══ -->
<div class="page-break"></div>
<h1 class="section-title">8 &nbsp; Infrastructure &amp; Deployment</h1>

<h2>Docker Build</h2>
<p>A <strong>multi-stage Dockerfile</strong> builds all 6 Go binaries in a <code>golang:1.26-alpine</code> builder stage, then copies only the compiled binaries into a minimal <code>alpine:latest</code> runtime image with <code>ca-certificates</code> and <code>ffmpeg</code>.</p>
<p>A single <code>start.sh</code> script launches all 6 services in the background, with the gateway as the process that keeps the container alive. Only <strong>port 8080</strong> is exposed.</p>

<h2>Deployment Options</h2>
<div class="card-grid">
  <div class="card-row">
    <div class="card">
      <span class="card-title">Google Cloud Run (current)</span>
      <span class="card-body">
        Single container, single port (8080). Cloud Build CI/CD via <code>cloudbuild.yaml</code>. Fully managed, auto-scales to zero. Firebase credentials injected via environment variables. Suitable for production deployment with minimal ops overhead.
      </span>
    </div>
    <div class="card">
      <span class="card-title">Kubernetes / K8s</span>
      <span class="card-body">
        Full Kubernetes deployment manifests in <code>k8s/deployments.yaml</code>. Each service as a separate Deployment. Horizontal Pod Autoscaler config in <code>k8s/hpa.yaml</code>. Secrets via Kubernetes Secrets (<code>sports-stream-secrets</code>). Suitable for multi-region or high-scale deployments.
      </span>
    </div>
  </div>
</div>

<h2>Infrastructure as Code</h2>
<p>Terraform configuration in <code>infra/terraform/</code> provisions GCP resources including Cloud Run services, Pub/Sub topics and subscriptions, Firestore database, and IAM bindings.</p>

<h2>Environment Variables</h2>
<table class="routes">
  <tr><th>Variable</th><th>Used By</th><th>Description</th></tr>
  <tr><td><code>GCP_PROJECT_ID</code></td><td>All services</td><td>GCP project identifier</td></tr>
  <tr><td><code>FIREBASE_CREDENTIALS</code></td><td>All services</td><td>Firebase service account JSON (can be file path or inline JSON)</td></tr>
  <tr><td><code>PORT</code></td><td>All services</td><td>HTTP listen port (auto-set by Cloud Run)</td></tr>
  <tr><td><code>ADMIN_PANEL_USERNAME</code></td><td>admin-service</td><td>Admin web UI username</td></tr>
  <tr><td><code>ADMIN_PANEL_PASSWORD</code></td><td>admin-service</td><td>Admin web UI password</td></tr>
  <tr><td><code>ADMIN_PANEL_SESSION_SECRET</code></td><td>admin-service</td><td>HMAC signing key for session cookies</td></tr>
  <tr><td><code>GCS_BUCKET</code></td><td>stream-service</td><td>Cloud Storage bucket for HLS segments</td></tr>
  <tr><td><code>CDN_BASE_URL</code></td><td>stream-service</td><td>Public CDN base URL for HLS manifest</td></tr>
  <tr><td><code>VIEWER_SUB</code></td><td>analytics-service</td><td>Pub/Sub subscription name for viewer events</td></tr>
  <tr><td><code>STREAM_SUB</code></td><td>analytics-service</td><td>Pub/Sub subscription name for stream events</td></tr>
  <tr><td><code>NOTIFICATION_SUB</code></td><td>notification-service</td><td>Pub/Sub subscription name for notifications</td></tr>
</table>


<!-- ══════════════════════════════════════════════════ SECTION 9 ══ -->
<h1 class="section-title">9 &nbsp; Observability &amp; Health</h1>

<div class="card-grid">
  <div class="card-row">
    <div class="card">
      <span class="card-title">Health Endpoint (/health)</span>
      <span class="card-body">
        The gateway probes all 5 microservices' <code>/health</code> endpoints (2-second timeout each). Each service health check verifies Firestore connectivity with a <code>Limit(1)</code> query.<br><br>
        Returns: HTML dashboard (browser) or JSON (API client). HTTP 503 if any service is down.
      </span>
    </div>
    <div class="card">
      <span class="card-title">Structured Logging</span>
      <span class="card-body">
        All services use <code>pkg/util.LogJSON()</code> for structured JSON log output. Each log line includes service name, level (info/error), event type, and contextual fields (requestId, uid, streamId).<br><br>
        Compatible with GCP Cloud Logging for centralized log aggregation and alerting.
      </span>
    </div>
  </div>
  <div class="card-row">
    <div class="card">
      <span class="card-title">Request Tracing</span>
      <span class="card-body">
        The gateway injects a unique <code>X-Request-Id</code> header on every request. If the client provides one, it is preserved. All service logs reference this request ID for distributed request tracing.
      </span>
    </div>
    <div class="card">
      <span class="card-title">Real-time Analytics SSE</span>
      <span class="card-body">
        The admin panel connects to <code>/api/v1/admin/analytics/sse</code> which streams Firestore real-time snapshots via Server-Sent Events. Analytics data updates in the browser without any polling — changes appear instantly as Pub/Sub events are processed.
      </span>
    </div>
  </div>
</div>

<div class="callout info">
  <strong>Load Testing:</strong> The project includes <code>k6-load-test.js</code> and <code>join_test.js</code> scripts for performance testing viewer join/leave concurrency and stream listing under load.
</div>


<!-- ══════════════════════════════════════════════════ SECTION 10 ══ -->
<div class="page-break"></div>
<h1 class="section-title">10 &nbsp; Future Roadmap — AWS Migration</h1>

<p>The project documentation outlines a planned migration from GCP to AWS. The architecture mapping is as follows:</p>

<table class="routes">
  <tr><th>Current (GCP)</th><th>Target (AWS)</th></tr>
  <tr><td>Firebase Auth + JWT</td><td>Amazon Cognito</td></tr>
  <tr><td>Cloud Firestore</td><td>Amazon DynamoDB</td></tr>
  <tr><td>Cloud Pub/Sub</td><td>Amazon SNS + SQS</td></tr>
  <tr><td>Firebase FCM</td><td>Amazon SNS Mobile Push</td></tr>
  <tr><td>GCP Transcoder API</td><td>AWS Elemental MediaConvert</td></tr>
  <tr><td>Cloud Storage (GCS)</td><td>Amazon S3 + CloudFront CDN</td></tr>
  <tr><td>Cloud Run / GKE</td><td>Amazon ECS / EKS</td></tr>
  <tr><td>Cloud Build</td><td>AWS CodePipeline / GitHub Actions</td></tr>
  <tr><td>Cloud Logging</td><td>Amazon CloudWatch Metrics &amp; Logs</td></tr>
</table>

<div class="callout info">
  <strong>Migration Strategy:</strong> The service-oriented architecture makes the migration incremental — each service can be migrated independently. The event-driven Pub/Sub pattern maps directly to SNS/SQS. The Firestore data model is document-oriented and can be translated to DynamoDB with appropriate partition key design.
</div>

<hr>
<h2>Summary</h2>
<p>
  Sports Stream Backend is a well-structured, production-ready live sports streaming platform that demonstrates modern backend engineering practices: microservice decomposition, event-driven architecture, idempotent operations, structured observability, and infrastructure-as-code. The system is designed to scale horizontally on both Cloud Run and Kubernetes, with clear paths for both vertical feature extension and horizontal cloud migration.
</p>

<div class="callout good">
  <strong>Key Engineering Highlights:</strong>
  Firestore transactions for race-condition-free viewer counting &bull;
  SHA-256 event fingerprinting for exactly-once analytics processing &bull;
  Multi-stage Docker build producing minimal Alpine images &bull;
  HMAC-signed session cookies with no external session store &bull;
  SSE-based real-time admin dashboard without WebSocket complexity &bull;
  Single container deployment simplifying Cloud Run ops while maintaining service isolation
</div>

<p style="text-align:center; color:#64748b; font-size:9pt; margin-top:24pt;">
  Sports Stream Backend — Technical Overview &bull; Generated May 2026 &bull; Confidential — Team Use Only
</p>

</body>
</html>
"""

css = CSS(string="""
@page { size: A4; margin: 18mm 15mm 18mm 15mm; }
""")

print("Generating PDF...")
HTML(string=html_content).write_pdf(
    "/Users/younus/Documents/sports-stream-backend-staging/Sports-Stream-Backend-Overview.pdf"
)
print("Done! PDF saved to Sports-Stream-Backend-Overview.pdf")
