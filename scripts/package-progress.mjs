// Human-readable progress stays separate from machine-readable receipts.
export function progress(label, records = []) {
  function start(stage) {
    const started = performance.now();
    console.log(`[${label}] ${stage}: started`);
    return (error) => {
      const record = { stage, status: error ? 'failed' : 'passed', duration_ms: Math.round(performance.now() - started) };
      records.push(record);
      console.log(`[${label}] ${stage}: ${record.status} (${(record.duration_ms / 1000).toFixed(2)}s)`);
    };
  }
  return {
    records, start,
    step(stage, fn) {
      const finish = start(stage);
      try { const result = fn(); finish(); return result; }
      catch (error) { finish(error); throw error; }
    },
    async run(stage, fn) {
      const finish = start(stage), started = performance.now();
      const heartbeat = setInterval(() => console.log(`[${label}] ${stage}: running (${((performance.now() - started) / 1000).toFixed(0)}s)`), 15000);
      heartbeat.unref();
      try { const result = await fn(); finish(); return result; }
      catch (error) { finish(error); throw error; }
      finally { clearInterval(heartbeat); }
    }
  };
}
