import { startPodder } from './bootstrap-policy.js';

void startPodder(window, document, () => import('./main.js'));
