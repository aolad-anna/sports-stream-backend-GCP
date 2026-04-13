import http from 'k6/http';
import { check, sleep } from 'k6';

const BASE_URL  = __ENV.BASE_URL  || 'https://sports-stream-backend-staging.osc-fr1.scalingo.io';
const STREAM_ID = __ENV.STREAM_ID || 'stream_1773883427477';
const FIREBASE_KEY = 'AIzaSyBZq6FQWa81-B4PPFqLA-Wh6orSi9xHws4';

// ── Stages ────────────────────────────────────────────────────────────────────
// Min 1: ramp to 5  users
// Min 2: ramp to 10 users
// Min 3: ramp to 15 users
// Min 4: ramp to 20 users
// 30min: hold at 20 — all watching, NO leave
// Last:  ramp to 0  — all leave at once

export const options = {
    stages: [
        { duration: '1m', target: 5  },   // wave 1 — 5 users join
        { duration: '1m', target: 10 },   // wave 2 — 10 users total
        { duration: '1m', target: 15 },   // wave 3 — 15 users total
        { duration: '1m', target: 20 },   // wave 4 — 20 users total
        { duration: '30m', target: 20 },  // hold   — all 20 watching
        { duration: '30s', target: 0  },  // ramp down — all leave
    ],
    thresholds: {
        http_req_duration: ['p(95)<2000'],
        http_req_failed:   ['rate<0.05'],
    },
};

// ── setup: health check + print test plan ─────────────────────────────────────

export function setup() {
    const res = http.get(`${BASE_URL}/health`);
    console.log(`Health check: ${res.status}`);
    console.log(`Stream: ${STREAM_ID}`);
    console.log('Plan: 20 unique users join in 4 waves, watch 30 min, all leave at end');

    // Get initial viewer count
    const streamRes = http.get(`${BASE_URL}/api/v1/streams/${STREAM_ID}`);
    if (streamRes.status === 200) {
        const data = JSON.parse(streamRes.body);
        console.log(`Initial viewerCount: ${data.data.viewerCount}`);
    }
}

// ── default: each VU = one unique anonymous Firebase user ─────────────────────

export default function () {

    // Each VU signs up as a new anonymous Firebase user — unique uid guaranteed
    const signupRes = http.post(
        `https://identitytoolkit.googleapis.com/v1/accounts:signUp?key=${FIREBASE_KEY}`,
        JSON.stringify({ returnSecureToken: true }),
        { headers: { 'Content-Type': 'application/json' } }
    );

    if (signupRes.status !== 200) {
        console.error(`VU ${__VU}: Firebase signup failed — ${signupRes.status}`);
        return;
    }

    const authData = JSON.parse(signupRes.body);
    const token    = authData.idToken;
    const uid      = authData.localId;

    console.log(`VU ${__VU}: signed in as uid=${uid}`);

    const headers = {
        'Authorization': `Bearer ${token}`,
        'Content-Type':  'application/json',
    };

    // ── JOIN stream ───────────────────────────────────────────────────────────
    const joinRes = http.post(
        `${BASE_URL}/api/v1/streams/${STREAM_ID}/join`,
        null,
        { headers }
    );

    const joined = check(joinRes, {
        'join status 200': (r) => r.status === 200,
    });

    if (joined) {
        const body       = JSON.parse(joinRes.body);
        const count      = body.data.viewerCount;
        console.log(`VU ${__VU} [uid=${uid}] JOINED — viewerCount: ${count}`);
    } else {
        console.error(`VU ${__VU} JOIN FAILED — ${joinRes.status}: ${joinRes.body}`);
        return;
    }

    // ── WATCH for 30 minutes — NO leave ──────────────────────────────────────
    // sleep keeps the VU alive and holding the join
    // k6 will call teardown/ramp-down at the end which triggers leave
    sleep(30 * 60);

    // ── LEAVE stream — only at ramp-down stage ────────────────────────────────
    const leaveRes = http.post(
        `${BASE_URL}/api/v1/streams/${STREAM_ID}/leave`,
        null,
        { headers }
    );

    check(leaveRes, {
        'leave status 200': (r) => r.status === 200,
    });

    if (leaveRes.status === 200) {
        const body  = JSON.parse(leaveRes.body);
        const count = body.data.viewerCount;
        console.log(`VU ${__VU} [uid=${uid}] LEFT — viewerCount: ${count}`);
    } else {
        console.error(`VU ${__VU} LEAVE FAILED — ${leaveRes.status}: ${leaveRes.body}`);
    }

    sleep(1);
}

// ── teardown: print final viewer count ───────────────────────────────────────

export function teardown() {
    sleep(5); // let Firestore settle

    const streamRes = http.get(`${BASE_URL}/api/v1/streams/${STREAM_ID}`);
    if (streamRes.status === 200) {
        const data  = JSON.parse(streamRes.body);
        const count = data.data.viewerCount;
        console.log('='.repeat(50));
        console.log('TEST COMPLETE');
        console.log(`Final viewerCount : ${count}`);
        console.log(`Expected          : 0`);
        console.log(`Result            : ${count === 0 ? 'PASS' : 'FAIL'}`);
        console.log('='.repeat(50));
        console.log('Check Firestore:');
        console.log(`  streams/${STREAM_ID}.viewerCount → 0`);
        console.log(`  analytics/${STREAM_ID}.currentViewers → 0`);
        console.log(`  analytics/${STREAM_ID}.peakViewers    → 20`);
        console.log(`  analytics/${STREAM_ID}.totalJoins     → 20`);
    }
}