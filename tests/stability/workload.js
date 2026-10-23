import exec from 'k6/execution';
import workload, { options as baseOptions } from '../performance/workload.js';

const seconds = Number.parseInt(__ENV.DURATION || '300s', 10);
if (!/^\d+s$/.test(__ENV.DURATION || '300s') || seconds < 1 || seconds > 600) {
  throw new Error('Stability duration must be 1..600 seconds');
}
export const options = { ...baseOptions, thresholds: { ...baseOptions.thresholds,
  http_req_failed: [{ threshold: 'rate<0.01', abortOnFail: true, delayAbortEval: '15s' }],
  http_req_duration: [{ threshold: 'p(95)<1000', abortOnFail: true, delayAbortEval: '15s' }],
  checks: ['rate==1'],
} };
// Bounded minute tags produce window summaries without writing request bodies,
// tokens or a per-request metric stream. k6 propagates VU tags to all metrics.
for (let window = 0; window < Math.ceil(seconds / 60); window++) {
  options.thresholds[`http_req_duration{window:${window}}`] = ['p(95)<1000'];
  options.thresholds[`http_reqs{window:${window}}`] = ['count>0'];
  options.thresholds[`business_success{window:${window}}`] = ['count>0'];
  options.thresholds[`http_req_failed{window:${window}}`] = ['rate<0.01'];
}
export default function () {
  exec.vu.metrics.tags.window = String(Math.min(Math.floor((Date.now() - exec.scenario.startTime) / 60000), Math.ceil(seconds / 60) - 1));
  workload();
}
