import http from 'k6/http';
import { check, sleep } from 'k6';

export const options = {
  stages: [
    { duration: '1m', target: 50 },   // ramp up to 50 VUs
    { duration: '3m', target: 50 },   // sustain 50 VUs
    { duration: '1m', target: 0 },    // ramp down
  ],
  thresholds: {
    http_req_duration: ['p(95)<500'],  // p95 < 500ms
    http_req_failed: ['rate<0.01'],    // error rate < 1%
  },
};

const BASE_URL = __ENV.BASE_URL || 'http://localhost:8080';

export default function () {
  const liveRes = http.get(`${BASE_URL}/live`);
  check(liveRes, {
    'live status is 200': (r) => r.status === 200,
    'live body contains ok': (r) => r.body.includes('ok'),
  });

  const readyRes = http.get(`${BASE_URL}/ready`);
  check(readyRes, {
    'ready status is 200 or 503': (r) => r.status === 200 || r.status === 503,
  });

  const metricsRes = http.get(`${BASE_URL}/metrics`);
  check(metricsRes, {
    'metrics status is 200': (r) => r.status === 200,
  });

  sleep(1);
}
