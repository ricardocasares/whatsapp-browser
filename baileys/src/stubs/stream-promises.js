export const pipeline = async () => { throw new Error('stream/promises is not available in the browser'); };
export const finished = pipeline;
export default { pipeline, finished };
