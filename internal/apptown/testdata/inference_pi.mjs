import assert from 'node:assert/strict';
import {pathToFileURL} from 'node:url';
import path from 'node:path';
import fs from 'node:fs/promises';

const root=process.env.APPTOWN_PI_MODULE;
const pkg=JSON.parse(await fs.readFile(path.join(root,'package.json'),'utf8'));
assert.equal(pkg.version,'0.84.3','test the pinned worker Pi version');
const {ModelRuntime}=await import(pathToFileURL(path.join(root,'dist/core/model-runtime.js')));
const runtime=await ModelRuntime.create({modelsPath:process.env.APPTOWN_PI_CONFIG,authPath:path.join(process.env.HOME,'auth.json'),modelsStorePath:path.join(process.env.HOME,'model-cache'),allowModelNetwork:false});
assert.equal(runtime.getError(),undefined);
for(const provider of ['openrouter','openai','anthropic','google','custom']){
 const model=runtime.getModels(provider).find(m=>provider==='custom' ? m.id==='allowed' : true);
 assert.ok(model,`no ${provider} model`);
 assert.ok(model.baseUrl.startsWith(process.env.APPTOWN_PI_PROXY+'/providers/'+provider), `${provider} bypassed proxy: ${model.baseUrl}`);
 const auth=await runtime.getAuth(model);
 assert.equal(auth.auth.apiKey,process.env.APPTOWN_SESSION_TOKEN,'Pi must send the session token it was given');
 const result=await runtime.completeSimple(model,{messages:[{role:'user',content:'hello',timestamp:Date.now()}]},{maxTokens:16});
 // The fixture returns a deliberate 418. This exercises Pi's real protocol
 // client, authentication, endpoint and error path without paid inference.
 assert.equal(result.stopReason,'error');
 assert.match(result.errorMessage,/418/,`${provider} failed before reaching the upstream: ${result.errorMessage}`);
 console.log(`${provider}: pinned Pi authenticated through AppTown`);
}
