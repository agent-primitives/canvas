import { CanvasError, createCanvas, joinSession } from '@github/copilot-sdk/extension';
import { startBridge } from './server.mjs';

const instances = new Map();
await joinSession({ canvases: [createCanvas({
  id: 'northwind-engine-review',
  displayName: 'Northwind Engine Review',
  description: 'Canvas engine: document review, separate simulated approval, explicit Continue and replay where available.',
  actions: [],
  open: async ctx => {
    const existing = instances.get(ctx.instanceId);
    if (existing) await existing.close();
    instances.delete(ctx.instanceId);
    try {
      const bridge = await startBridge();
      instances.set(ctx.instanceId, bridge);
      return { title: 'Northwind review', status: 'Provider mode and identity are shown in the review panel.', url: bridge.url };
    } catch {
      throw new CanvasError('engine_unavailable', 'Start the separate Canvas engine and check CANVAS_ENGINE_CONFIG when configured (HTTPS loopback URL and CA file). Otherwise start local copilot-review on 127.0.0.1:18878. Reopen this canvas.');
    }
  },
  onClose: async ctx => {
    const bridge = instances.get(ctx.instanceId);
    instances.delete(ctx.instanceId);
    if (bridge) await bridge.close();
  },
})] });
