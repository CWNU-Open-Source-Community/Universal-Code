import {APIClient} from "./client";
import {listMCPInputs, respondMCPInput, type MCPInput} from "./mcp-inputs";
const input={run_id:"run-1",id:"input-1"} as MCPInput;
afterEach(()=>vi.unstubAllGlobals());
it.each(["AUTH.EXAMPLE", "auth.example:443"])("accepts the same HTTPS host after browser normalization: %s", async (host) => {
 const pending = {id:"input-1",run_id:"run-1",mode:"url",host,url:`https://${host}/authorize?state=one-time`,fields:null,expires_at:new Date(Date.now()+60_000).toISOString()};
 vi.stubGlobal("fetch",vi.fn().mockResolvedValue(new Response(JSON.stringify({version:"api.v1",request_id:"request",data:{version:"mcp-input.v1",items:[pending]}}))));
 expect(await listMCPInputs(new APIClient("read"),"run-1")).toEqual([pending]);
});
it("rejects a URL whose displayed host is different", async () => {
 const pending = {id:"input-1",run_id:"run-1",mode:"url",host:"trusted.example",url:"https://other.example/authorize",fields:null,expires_at:new Date(Date.now()+60_000).toISOString()};
 vi.stubGlobal("fetch",vi.fn().mockResolvedValue(new Response(JSON.stringify({version:"api.v1",request_id:"request",data:{version:"mcp-input.v1",items:[pending]}}))));
 await expect(listMCPInputs(new APIClient("read"),"run-1")).rejects.toThrow("external authorization URL");
});
it("sends one live answer with extension control even when run control is disabled",async()=>{
 const fetch=vi.fn().mockResolvedValue(new Response(JSON.stringify({version:"api.v1",request_id:"request",data:{accepted:true}})));
 vi.stubGlobal("fetch",fetch);
 const client=new APIClient("read","/api/v1","control",{extensionControlEnabled:true,runControlEnabled:false});
 await respondMCPInput(client,input,"accept",{color:"blue"});
 expect(fetch).toHaveBeenCalledOnce();
 const [path,options]=fetch.mock.calls[0]!;
 expect(path).toBe("/api/v1/runs/run-1/mcp-inputs/input-1");
 expect(options.method).toBe("POST");expect(options.headers.Authorization).toBe("Bearer control");
 expect(options.headers["Idempotency-Key"]).toBeUndefined();
 expect(JSON.parse(options.body)).toEqual({version:"mcp-input.v1",action:"accept",content:{color:"blue"}});
});
it("does not substitute run control for extension control",async()=>{
 const fetch=vi.fn();vi.stubGlobal("fetch",fetch);
 const client=new APIClient("read","/api/v1","control",{extensionControlEnabled:false,runControlEnabled:true});
 await expect(respondMCPInput(client,input,"decline")).rejects.toThrow("extension control");expect(fetch).not.toHaveBeenCalled();
});
it("never retries an uncertain live answer",async()=>{
 const fetch=vi.fn().mockRejectedValue(new TypeError("connection lost"));vi.stubGlobal("fetch",fetch);
 const client=new APIClient("read","/api/v1","control",{extensionControlEnabled:true});
 await expect(respondMCPInput(client,input,"cancel")).rejects.toThrow("connection lost");expect(fetch).toHaveBeenCalledOnce();
});
