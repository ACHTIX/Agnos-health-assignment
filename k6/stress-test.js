import http from 'k6/http';
import { check, sleep } from 'k6';

export const options = {
  stages: [
    { duration: '1m', target: 50 },    // warm up
    { duration: '2m', target: 100 },   // ramp to moderate load
    { duration: '2m', target: 200 },   // ramp to stress level
    { duration: '2m', target: 200 },   // sustain stress
    { duration: '1m', target: 0 },     // ramp down
  ],
  thresholds: {
    http_req_duration: ['p(95)<500'],   // p95 < 500ms
    http_req_failed: ['rate<0.01'],     // error rate < 1%
  },
};

const BASE_URL = __ENV.BASE_URL || 'http://localhost:8080';

export default function () {
  const liveRes = http.get(`${BASE_URL}/live`);
  check(liveRes, {
    'live status is 200': (r) => r.status === 200,
  });

  const readyRes = http.get(`${BASE_URL}/ready`);
  check(readyRes, {
    'ready status is 200 or 503': (r) => r.status === 200 || r.status === 503,
  });

  const metricsRes = http.get(`${BASE_URL}/metrics`);
  check(metricsRes, {
    'metrics status is 200': (r) => r.status === 200,
  });

  sleep(0.5);
}
