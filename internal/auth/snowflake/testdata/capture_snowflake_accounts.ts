// Runs NeoPi's OWN Snowflake account handling (pi-ai registry/snowflake.ts):
// normalizeSnowflakeAccountUrl over account inputs, and
// snowflakeTransport.prepareRequest over endpoint, key and SNOWFLAKE_ACCOUNT
// combinations. Records each result or the error it throws.
//
//   cd <neopi>/env && HOME=$PWD/home NEOPI_SOURCE_MODULES=$PWD/node_modules/@oh-my-pi \
//     bun --preserve-symlinks capture_snowflake_accounts.ts > snowflake_accounts.json
const root = process.env.NEOPI_SOURCE_MODULES;
if (!root) throw new Error("NEOPI_SOURCE_MODULES is required");
const { normalizeSnowflakeAccountUrl, snowflakeTransport } = await import(root + "/pi-ai/src/registry/snowflake.ts");

const accounts = [
	"myorg-myaccount",
	"  MyOrg_MyAccount  ",
	"myorg-myaccount.snowflakecomputing.com",
	"myorg-myaccount.snowflakecomputing.com:443",
	"myorg-myaccount.eu-west-1.snowflakecomputing.com",
	"https://myorg-myaccount.snowflakecomputing.com",
	"https://MYORG-MYACCOUNT.snowflakecomputing.com:443/console?x=1#y",
	"https://app.snowflake.com/myorg/myaccount/worksheets",
	"https://app.snowflake.com/MyOrg/My_Account",
	"https://app.snowflake.com/us-east-1/xy12345",
	"https://app.snowflake.com/us-gov-west-1/xy12345",
	"https://app.snowflake.com/myorg",
	"https://app.snowflake.com/",
	"http://myorg-myaccount.snowflakecomputing.com",
	"ftp://myorg-myaccount",
	"HTTPS://myorg-myaccount.snowflakecomputing.com",
	"myorg-myaccount.snowflakecomputing.cn",
	"https://myorg-myaccount.snowflakecomputing.cn",
	"myorg/myaccount",
	"myorg?x",
	"myorg#x",
	"my\\org",
	"https://evil.example.com",
	"https://evil.example.com/.snowflakecomputing.com",
	".snowflakecomputing.com",
	"-bad-.snowflakecomputing.com",
	"a".repeat(64),
	"a".repeat(63),
	"myorg..myaccount",
	"https://",
	"",
	"   ",
	"host:443",
	"https://user@myorg-myaccount.snowflakecomputing.com",
];
const normalize = accounts.map(input => {
	try {
		return { input, url: normalizeSnowflakeAccountUrl(input) };
	} catch (error) {
		return { input, error: (error as Error).message };
	}
});

const placeholder = "https://snowflake-account.invalid";
const bases = [placeholder + "/api/v2/cortex", placeholder + "/api/v2/cortex/v1", placeholder, "https://myorg-myaccount.snowflakecomputing.com/api/v2/cortex", "https://other-account.snowflakecomputing.com/api/v2/cortex", "not a url"];
const keys = [
	"pat-token",
	"  pat-token  ",
	"",
	JSON.stringify({ token: "oauth-token", enterpriseUrl: "myorg-myaccount" }),
	JSON.stringify({ token: " oauth-token ", enterpriseUrl: "https://app.snowflake.com/myorg/myaccount" }),
	JSON.stringify({ token: "oauth-token" }),
	JSON.stringify({ token: "", enterpriseUrl: "myorg-myaccount" }),
	JSON.stringify({ token: "oauth-token", enterpriseUrl: "acct.snowflakecomputing.cn" }),
	JSON.stringify({ token: 5, enterpriseUrl: "myorg-myaccount" }),
	"{not json",
	"null",
	"[]",
];
const envAccounts = [undefined, "env-org-env-account", "bad/account"];
const transport: any[] = [];
for (const base of bases) {
	for (const apiKey of keys) {
		for (const envAccount of envAccounts) {
			if (envAccount === undefined) delete process.env.SNOWFLAKE_ACCOUNT;
			else process.env.SNOWFLAKE_ACCOUNT = envAccount;
			delete process.env.SNOWFLAKE_PAT;
			const model = { id: "m", baseUrl: base };
			try {
				const prepared = snowflakeTransport.prepareRequest(model, { apiKey });
				transport.push({ base, apiKey, envAccount: envAccount ?? null, baseUrl: prepared.model.baseUrl, token: prepared.options.apiKey ?? null });
			} catch (error) {
				transport.push({ base, apiKey, envAccount: envAccount ?? null, error: (error as Error).message });
			}
		}
	}
}
console.log(JSON.stringify({ source: "NeoPi OWN pi-ai registry/snowflake.ts", placeholder, normalize, transport }, null, "\t"));
