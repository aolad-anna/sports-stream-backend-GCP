import http from 'k6/http';
import { check, sleep } from 'k6';
import { Rate, Trend } from 'k6/metrics';

const successRate = new Rate('success_rate');
const listDuration = new Trend('list_duration_ms');

const BASE_URL  = __ENV.BASE_URL  || 'https://sports-stream-backend-staging.osc-fr1.scalingo.io';
const STREAM_ID = __ENV.STREAM_ID || 'stream_1773883427477';
const TOKEN     = __ENV.TOKEN     || 'eyJhbGciOiJSUzI1NiIsImtpZCI6IjczMmNhOTY3MTNiMWRkMTcyMzg1MDg0Y2U5ZjQzODFhZDAwY2VjZTQiLCJ0eXAiOiJKV1QifQ.eyJuYW1lIjoiTWFkIFVzZXIiLCJpc3MiOiJodHRwczovL3NlY3VyZXRva2VuLmdvb2dsZS5jb20vc3BvcnRzLXN0cmVhbS02NjU1MyIsImF1ZCI6InNwb3J0cy1zdHJlYW0tNjY1NTMiLCJhdXRoX3RpbWUiOjE3NzQyNjc2NzMsInVzZXJfaWQiOiJiZTVSMzJYaVNnVlZubHdaZWhVVWFRZ3pJNWIyIiwic3ViIjoiYmU1UjMyWGlTZ1ZWbmx3WmVoVVVhUWd6STViMiIsImlhdCI6MTc3NDI2NzY3MywiZXhwIjoxNzc0MjcxMjczLCJlbWFpbCI6Im1hZEBnbWFpbC5jb20iLCJlbWFpbF92ZXJpZmllZCI6ZmFsc2UsImZpcmViYXNlIjp7ImlkZW50aXRpZXMiOnsiZW1haWwiOlsibWFkQGdtYWlsLmNvbSJdfSwic2lnbl9pbl9wcm92aWRlciI6InBhc3N3b3JkIn19.ICGq1RhgQ7jI3mZk1jmKkUnNQSM2Bu8tMG6WM313QT2LVh5dKXpURiQDg55gA8pC08KZ_cqjlgQFuNqzlpzCRneEW_Qqw3yKc_YeIurxQDV5AAsH_Cj3hcAyY6t3q4NRryjI7jhW0Fl_R2peTAr3jVh8edBIzLzJXSFwSHU8KhAroqXs7vSQA3W0_N5OCcF2qSCr3c7Uy19We0b7rJE_SAcWJnYFz3VvIWS5j1mBVADr3tS-3FUYjqjfZLKo4hhNrQ2Uk_19F4QSSB74wM3uQbotgTx5aCfhTtoSk1zBcBb0ouwbQFSXZrgfVxJQrDjCUxsd78LAaQ-dNrh8wfg-hQ';

export const options = {
    stages: [
        { duration: '1m',  target: 500  },
        { duration: '2m',  target: 2000 },
        { duration: '3m',  target: 5000 },
        { duration: '2m',  target: 5000 },
        { duration: '1m',  target: 0    },
    ],
    thresholds: {
        http_req_duration: ['p(95)<3000'],
        http_req_failed:   ['rate<0.10'],
    },
};

export function setup() {
    const res = http.get(BASE_URL + '/health');
    console.log('Health check: ' + res.status);
}

export default function () {
    const headers = TOKEN
        ? { Authorization: 'Bearer ' + TOKEN }
        : {};

    const start = Date.now();
    const res = http.get(BASE_URL + '/api/v1/streams', { headers });
    listDuration.add(Date.now() - start);

    const ok = check(res, {
        'status 200': (r) => r.status === 200,
    });
    successRate.add(ok);
    sleep(1);

    if (TOKEN) {
        http.post(BASE_URL + '/api/v1/streams/' + STREAM_ID + '/join', null, { headers });
        sleep(Math.random() * 5 + 2);
        http.post(BASE_URL + '/api/v1/streams/' + STREAM_ID + '/leave', null, { headers });
    } else {
        sleep(Math.random() * 2 + 1);
    }
}