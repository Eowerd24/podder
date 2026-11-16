// Delegation supports both static markup and rows rendered after startup.
// Only named callbacks in this closed table can run; attribute data is not code.
export function bindActions(root, actions) {
    for (const type of ['click', 'change', 'input']) {
        root.addEventListener(type, event => {
            const element = event.target.closest(`[data-${type}]`);
            if (!element || element.disabled) return;
            const action = element.getAttribute(`data-${type}`);
            if (!Object.hasOwn(actions, action)) return;
            let args;
            try { args = JSON.parse(element.getAttribute(`data-${type}-args`) || '[]'); }
            catch { return; }
            if (!Array.isArray(args)) return;
            const field = element.getAttribute(`data-${type}-field`);
            if (field === 'value' || field === 'checked') args.push(element[field]);
            actions[action](...args);
        });
    }
}
