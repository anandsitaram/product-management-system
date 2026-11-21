// k6 load test for read endpoints:  k6 run -e BASE_URL=http://localhost:8080 -e USER=admin -e PASS=... scripts/loadtest.js
import http from 'k6/http';
import { check, sleep } from 'k6';

export const options = {
  stages: [{ duration: '30s', target: 20 }, { duration: '1m', target: 20 }, { duration: '10s', target: 0 }],
  thresholds: { http_req_failed: ['rate<0.01'], http_req_duration: ['p(95)<300'] },
};
const BASE = __ENV.BASE_URL || 'http://localhost:8080';

export function setup() {
  const r = http.post(`${BASE}/api/v1/auth/token`, JSON.stringify({ username: __ENV.USER || 'admin', password: __ENV.PASS }),
    { headers: { 'Content-Type': 'application/json' } });
  return { token: r.json('data.access_token') };
}

export default function (d) {
  const h = { headers: { Authorization: `Bearer ${d.token}` } };
  for (const q of ['', '?search=widget', '?sort_by=price&order=asc&limit=50', '?status=ACTIVE&min_price=1&max_price=100']) {
    const r = http.get(`${BASE}/api/v1/products${q}`, h);
    check(r, { 'status 200': (x) => x.status === 200 });
  }
  sleep(1);
}
